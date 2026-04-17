# Design: gopls CLI flag & help overhaul

Status: draft
Author: (fill in)
Date: 2026-04-16
Branch: `cli-flags-overhaul` (local prototype; not for upstream yet)

## Problem

The gopls command line has two related papercuts.

**Flag-parsing papercut.** gopls rejects global flags when placed after the
subcommand. `gopls version -v` fails with
`flag provided but not defined: -v`, while `gopls -v version` works. Users
coming from `go build -v`, `git log -v`, etc. find this surprising. The
exception is `serve`, whose flags happen to work in both positions because
`Serve` is embedded in `Application`.

**Help papercut.** The golden help output in `gopls/internal/cmd/usage/*.hlp`
has three related issues:

1. Top-level `gopls help` mixes global flags (`-v`, `-vv`, `-remote`, `-otel`,
   `-profile.*`) with `serve`-only flags (`-debug`, `-listen`, `-logfile`, …)
   in one alphabetized block, because `Serve` is embedded in `Application`.
2. Every subcommand help page has `Usage: gopls [flags] <cmd>` but does not
   list what `[flags]` contains — there is no way to discover `-v` from
   `gopls help format`.
3. Minor: `featureCommands()` ordering isn't strictly alphabetical; usage-line
   conventions vary across subcommands; `help`'s own usage line omits
   `<subject>`.

This document proposes a phased overhaul that fixes both together.

## Goals

- Accept all global flags (`-v`, `-vv`, `-remote`, `-otel`, `-profile.*`) in
  either position: before *or* after the subcommand name.
- Make subcommand help pages list the global flags they now accept.
- Split global flags from `serve`-only flags in the top-level help.
- Polish command list ordering and usage-line consistency.
- Centralize the change; require zero edits to existing subcommand source.
- Preserve full backward compatibility: every command line that worked
  before still works.

## Non-goals

- **Interleaving non-flag args with flags.** `gopls format foo.go -v` stays
  an error (stdlib `flag` stops at the first non-flag). Users can write
  `gopls format -v foo.go`.
- **Short-flag grouping (`-vvr`)** beyond what stdlib already supports.
- **GNU-style `--long=value` forms** beyond what stdlib accepts.
- **Introducing/removing/renaming flags.** Every flag name, help text, and
  semantic stays as it is today.
- **Adopting an external CLI library.** Stay within the `x/tools` module
  and the existing `internal/tool` harness.
- **Cleaning up latent issues like the `references.go` `-d` ambiguity
  with `EditFlags.-d,diff`.** That is orthogonal.

## Background: how parsing works today

gopls's main is driven by `internal/tool/tool.go`, a reflection-based
wrapper over `flag.FlagSet`. Parsing happens in two stages:

1. **Application stage** (`internal/tool/tool.go:110-127`): a `FlagSet` is
   populated from `flag:"..."` tags on `Application`
   (`gopls/internal/cmd/cmd.go:43-77`). `s.Parse(args)` runs; the stdlib
   `flag` package stops at the first non-flag argument, so parsing halts
   at the subcommand name.
2. **Subcommand dispatch** (`gopls/internal/cmd/cmd.go:244-250`): the
   first remaining arg is the subcommand name; a **fresh** `FlagSet` is
   created, populated from the subcommand struct's own `flag:"..."` tags
   *only*, and `tool.Run` is called recursively.

Because stage 2's FlagSet has only the subcommand's declared flags, any
global flag in the residual args is rejected. `Serve` is an accidental
exception because it is embedded in `Application`, so its flag tags
register at both the top level and as the `serve` subcommand.

Flag-name audit (`rg 'flag:"' gopls/internal/cmd`): no subcommand declares
any of `v`, `verbose`, `vv`, `veryverbose`, `remote`, `otel`, or
`profile.*`. Inheritance is safe today.

## Design

### Core mechanism: `tool.RunInherited`

Add one new public entry point to `internal/tool/tool.go`:

```go
// RunInherited is like Run, but first registers the flag-tagged fields of
// parent onto s (using the parent's own pointer targets), then the
// flag-tagged fields of app. A subcommand's flag shadows a parent flag of
// the same name — the parent's registration is skipped for that name.
func RunInherited(ctx context.Context, s *flag.FlagSet, app, parent Application, args []string) error
```

Implementation: factor the existing `addFlags` call in `Run` into a shared
path; in `RunInherited`, call `addFlags` on `parent` first, then on `app`
with a "skip if name already taken" guard. Parse and dispatch as today.
Everything else in `Run` (profile setup, error handling) is unchanged.

`Run` stays exactly as it is, so other callers of `internal/tool` in
`x/tools` (if any outside gopls) are unaffected.

### Wiring into gopls

`gopls/internal/cmd/cmd.go:244-250` becomes:

```go
for _, c := range app.Commands() {
    if c.Name() == command {
        s := flag.NewFlagSet(app.Name(), flag.ExitOnError)
        return tool.RunInherited(ctx, s, c, app, args)
    }
}
```

The no-args path at `cmd.go:241-242` stays on `tool.Run` with `&app.Serve`
(it already works because of embedding), or can be migrated for symmetry.

### Help rendering

Two orthogonal changes:

- **Subcommand help:** after inheritance, `printFlagDefaults`
  (`cmd.go:161-212`) naturally emits global flags in each subcommand's
  help. First cut: mix inline with the subcommand's own flags. If noisy,
  follow up with a two-section render ("flags" + "global flags") by
  teaching `printFlagDefaults` a "local-only" predicate, with
  `RunInherited` recording inherited names on a side map keyed by the
  FlagSet pointer.
- **Top-level help:** `(*Application).DetailedHelp` (`cmd.go:122-158`)
  switches from a single `flags:` block to two labeled sections, "global
  flags" and "serve flags", classified by whether the flag target
  belongs to `app` directly or to `app.Serve`.

### Flag-name collision policy

If a subcommand declares a flag name that matches a global,
`RunInherited` registers the subcommand's flag and skips the parent's for
that name. This is a conservative "subcommand wins" rule that avoids a
startup panic and preserves today's behavior for the pre-subcommand
position (where the global is parsed at the Application stage). Unit
test locks this in.

## Phases

Seven commits across four phases. Each phase is independently reviewable,
independently revertable, and passes `go test ./...`, `gofmt -l`, and
`go fix ./...` on its own. Phase dependencies flow strictly downward.

### Phase 0 — Harness prep (no behavior change)

**Commit 1: `internal/tool: add RunInherited for flag inheritance`**

- Add `tool.RunInherited` to `internal/tool/tool.go`.
- Refactor `addFlags` call sites so `Run` and `RunInherited` share the
  registration path; add a name-collision guard.
- Add unit tests in `internal/tool/tool_test.go` (create if absent):
  - Parent flag registered and writeable through parent pointer.
  - Subcommand flag registered and writeable through subcommand pointer.
  - Name collision → subcommand wins; parent flag not registered.
  - Help output lists both parent and subcommand flags.
  - Profile fields on parent still honored if `parent` embeds
    `tool.Profile`.

**Touches:** `internal/tool/tool.go`, new `internal/tool/tool_test.go`.
**Behavior change:** none. gopls still calls `tool.Run`.
**Golden files:** unchanged.
**Risk:** low — additive API with local unit tests.

### Phase 1 — Enable inheritance in gopls (headline change)

**Commit 2: `gopls/internal/cmd: inherit global flags into subcommands`**

- Swap `tool.Run` → `tool.RunInherited` at `cmd.go:247`.
- Regenerate `gopls/internal/cmd/usage/*.hlp` via
  `go test -run Help -update-help-files ./gopls/internal/cmd/`.
  Every subcommand `.hlp` gains lines for `-v`, `-vv`, `-remote`,
  `-otel`, `-profile.*`.
- Add CLI-level tests in `gopls/internal/cmd/integration_test.go` (or a
  new `flags_test.go`):
  - `gopls version -v` succeeds and emits verbose output.
  - `gopls -v version` produces the same output (parity check).
  - `gopls format -v -l <file>` sets both.
  - `gopls <cmd> -notaflag` still fails.
  - `gopls help version` lists the global flags.

**Touches:** `gopls/internal/cmd/cmd.go` (1 line), every
`gopls/internal/cmd/usage/*.hlp` (mechanical regen), one test file.
**Behavior change:** YES — post-subcommand globals now accepted.
**Golden files:** ~31 files regenerate with the globals block appended.
**Risk:** low-medium — behavior change is small and additive. Watch for
flag-name collisions surfaced by startup panics (audit says none exist).

**Commit 3 (optional, bundle with commit 2 if small):
`gopls/internal/cmd: render inherited flags in a separate section`**

- Extend `printFlagDefaults` to accept an optional "inherited names" set;
  render `flags:` first, then `global flags:` below.
- `RunInherited` records inherited names; `printFlagDefaults` pulls them
  out.
- Regenerate goldens.

Ship this as a separate commit only if reviewers find the inline mix
noisy. Trivial to drop.

### Phase 2 — Top-level help split

**Commit 4: `gopls/internal/cmd: separate global and serve flags in
top-level help`**

- Refactor `(*Application).DetailedHelp` at `cmd.go:122-158` to emit two
  labeled sections, classifying each flag by whether its target pointer
  is on `app` itself or on `app.Serve`. A reflection pass classifies.
- Regenerate `usage.hlp` and `usage-v.hlp`.
- Add a test that asserts the `global flags:` and `serve flags:` section
  headers are both present in `usage.hlp`.

**Touches:** `gopls/internal/cmd/cmd.go`, 2 golden files, 1 test.
**Behavior change:** help text only.
**Golden files:** 2 regenerate with real content changes (split + labels).
**Risk:** low — help-only; classification logic is straightforward
reflection.

### Phase 3 — Polish

**Commit 5: `gopls/internal/cmd: sort command lists alphabetically`**

- `sort.SliceStable` on `mainCommands()` / `featureCommands()` /
  `internalCommands()` output by `Name()`, OR re-order the slice literals
  by hand (simpler, zero runtime cost).
- Regenerate `usage.hlp` / `usage-v.hlp`.

**Touches:** `gopls/internal/cmd/cmd.go`, 2 goldens.
**Risk:** trivial.

**Commit 6: `gopls/internal/cmd: normalize subcommand usage lines`**

- Drop `[format-flags]`, `[rename-flags]`, `[mcp-flags]`, `[server-flags]`
  suffixes in the `Usage()` / `DetailedHelp` strings; standardize on
  `gopls [flags] <cmd> [args...]`. `[flags]` now truly covers everything
  visible on the help page.
- Regenerate affected goldens.

**Touches:** `gopls/internal/cmd/{format,rename,mcp,serve}.go` (text
only), ~5 goldens.
**Risk:** trivial.

**Commit 7: `gopls/internal/cmd: show <subject> in help's usage line`**

- Set `(*help).Usage()` at `info.go:36` to return `"[<subject>]"`.
- Regenerate `help.hlp`.

**Touches:** `gopls/internal/cmd/info.go` (1 line), 1 golden.
**Risk:** trivial.

### Phase dependency graph

```
Phase 0 (C1)
   └── Phase 1 (C2, optionally C3)
            ├── Phase 2 (C4)          [parallel possible]
            └── Phase 3 (C5, C6, C7)  [parallel possible]
```

Phases 2 and 3 can be developed in parallel after Phase 1 lands, or
skipped entirely without breaking Phase 1. Within Phase 3, C5/C6/C7 are
fully independent and can be re-ordered.

## Test plan

### Per-commit invariants (run on every commit)

```
gofmt -l gopls/internal/cmd internal/tool | grep -q . && echo FAIL || echo OK
go test ./gopls/internal/cmd/... ./internal/tool/...
```

Any non-empty `gofmt -l` output or test failure blocks the commit.
`go fix` is intentionally not part of this loop — it is handled
separately, repo-wide, outside this project.

### Per-phase functional coverage

**Phase 0** — unit tests in `internal/tool/tool_test.go`:

- Parent-only flag parses and writes target.
- Subcommand-only flag parses and writes target.
- Parent + subcommand both present, no collision: both parse.
- Collision: subcommand wins, parent flag not registered.
- `-h` output contains both parent and subcommand flags.
- Embedded `Profile` on parent still registers `profile.*` flags.

**Phase 1** — CLI integration tests in `gopls/internal/cmd/`:

- `gopls version -v` exit 0, stdout matches verbose format.
- `gopls -v version` stdout identical to the above.
- `gopls format -v -l <tmpfile>` exits 0, both flags reflected in output.
- `gopls <cmd> -notaflag` exits 2 with the usual error.
- `gopls help version` stdout contains `-v,-verbose` and `-remote`.
- Flaky-test audit: any existing test that ran `gopls <cmd> -h` and
  asserted on help content needs updating to the new goldens.

**Phase 2** — assert `usage.hlp` contains both `global flags:` and
`serve flags:` headers; assert `-v` is under `global flags` and
`-logfile` is under `serve flags`.

**Phase 3** — golden comparisons cover ordering, usage-line strings,
and `help`'s `<subject>`.

### Golden-file regeneration

The existing `TestHelpFiles` in `gopls/internal/cmd/help_test.go:33`
drives regeneration:

```
go test -run Help -update-help-files ./gopls/internal/cmd/
```

Every commit that touches help output re-runs this and commits the
updated `.hlp` files in the same commit as the source change.

## Impact estimation

### Source LOC

| Phase | Files | LOC added | LOC changed | LOC removed |
|-------|-------|-----------|-------------|-------------|
| 0     | 2     | ~60 (impl + tests) | 0   | 0           |
| 1     | 2-3   | ~20 (tests) | 1  | 0           |
| 2     | 2     | ~30       | ~20         | 0           |
| 3 (C5)| 1     | ~5        | ~5          | 0           |
| 3 (C6)| 4-5   | 0         | ~10         | 0           |
| 3 (C7)| 1     | 1         | 1           | 0           |

Total source change: under ~120 LOC added, under ~40 LOC changed.

### Golden-file churn

| Phase | Files affected | Nature |
|-------|----------------|--------|
| 1     | ~31 (all `usage/*.hlp`) | Append globals block to each subcommand page |
| 2     | 2 (`usage.hlp`, `usage-v.hlp`) | Substantive: split into two sections |
| 3 C5  | 2 (`usage.hlp`, `usage-v.hlp`) | Command-list reordering |
| 3 C6  | ~5 (`format`, `rename`, `mcp`, `serve`, …) | Usage-line text |
| 3 C7  | 1 (`help.hlp`) | Usage-line text |

### Subcommand source impact

**Zero changes required** to any of the ~31 subcommand source files
except the four touched by C6 (`format.go`, `rename.go`, `mcp.go`,
`serve.go`) — and those only edit their `DetailedHelp` string literals,
not logic.

### User impact

- **CLI users:** strict improvement. Previously-failing command lines
  now work; previously-working ones are unchanged.
- **Script callers:** scripts that depended on `gopls <cmd> -v` *failing*
  will change behavior. Unlikely to exist in practice — nobody writes
  a script that asserts on an error the CLI ought not to produce — but
  worth calling out in release notes.
- **Editors (VS Code, etc.):** they invoke `gopls serve` or specific
  subcommands with flags upfront. No impact.
- **Help-scrapers:** any tooling that parses `gopls help` output will
  see new content. Not a stable surface.

## Compatibility

- Fully backward-compatible at the CLI level: every old command line
  still works.
- No change to LSP protocol, daemon behavior, or any in-process API.
- No change to the public surface of `x/tools/internal/tool` — only an
  additive `RunInherited` function.
- Release notes entry: "gopls CLI: global flags (`-v`, `-vv`,
  `-remote`, `-otel`, `-profile.*`) now work when placed after the
  subcommand name, in addition to before it."

## Rollback

Each commit is a single logical unit revertable with `git revert`:

- Reverting C7/C6/C5 restores cosmetic text only.
- Reverting C4 restores the combined-flags top-level help.
- Reverting C2 (+C3) restores pre-subcommand-only global flags; no users
  are broken because the pre-subcommand form always worked.
- Reverting C1 removes the unused `tool.RunInherited`.

No migration scripts or data changes are involved anywhere.

## Alternatives considered

1. **Error-message improvement only.** Detect a known global in the
   subcommand-flag error and hint "place it before the subcommand."
   Cheap (~10 LOC), no behavior change. Rejected as the primary fix
   because it codifies the papercut rather than removing it; could ship
   as a complement to C2 for unknown-global future-proofing.
2. **Full pre-scan / reorder.** Walk `os.Args` once, split into globals,
   subcommand, sub-flags; parse each cleanly. Allows interleaving
   (`gopls format foo.go -v`). Rejected: more code for marginal value;
   drifts toward GNU-style semantics that the project has chosen not to
   adopt.
3. **Declare globals on every subcommand struct manually.** No harness
   change. Rejected: 31 structs to edit now, and every future global
   requires 31 more edits.
4. **Adopt `cobra`/`kong`/`urfave/cli`.** Rejected: the
   no-public-LSP/CLI-deps constraint for this module; migration cost
   dwarfs the benefit.

## Open questions

- **C3 (two-section rendering) or inline mix?** Decide after seeing C2's
  golden-file diff. If subcommand pages look tidy, skip C3.
- **Migrate the no-args `serve` path to `RunInherited` too?** It already
  works via embedding. Touching it only for symmetry costs one
  golden-file change and avoids future drift. Lean yes, but defer to C2.
- **Should globals appear in sorted position or as a trailing block in
  subcommand help?** `printFlagDefaults` currently sorts by
  `VisitAll` order (which is alpha from `FlagSet`). Inline sort keeps
  things uniform; trailing block requires the C3 refactor.
- **Release-note placement.** If this lands in a gopls minor release
  (e.g., v0.20), add a CLI-changes bullet; otherwise note in
  `gopls/doc/release/`.

## Local workflow (prototype)

Branch: `cli-flags-overhaul` (local only; do not push).

```
# per commit
<make edits>
cd gopls && go test -run Help -update-help-files ./internal/cmd/   # if help affected
cd .. && gofmt -w gopls/internal/cmd internal/tool
go test ./gopls/internal/cmd/... ./internal/tool/...
git add -p
git commit
```

No `Co-Authored-By` trailers. No force-push. No PR until the branch is
promoted from prototype.

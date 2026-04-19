---
name: goplscli
description: Use `gopls cli` for Go code intelligence — go-to-definition, find-references, hover, find-implementations, rename with dry-run preview, symbol search, diagnostics (check/vet), and code-action-driven edits (format/imports/codeaction/fix). Use when navigating Go code, understanding types/APIs, collecting compile/vet diagnostics, or applying single-shot edits safely. Faster and more accurate than grep for type-aware queries.
allowed-tools: Bash(gopls *)
---

# gopls cli — Code Intelligence for Go

`gopls cli` provides type-aware Go code navigation, diagnostics, and
edit operations through shell commands. Use it instead of grep when you
need to resolve identifiers, find references, read type signatures,
preview a rename, or run the same checks your editor surfaces.

Connects to a gopls daemon over standard LSP. The daemon is shared with your
editor (if one is running) or auto-spawned on first use.

## Setup

The daemon must have session pooling enabled for acceptable per-query
latency. Start it once (or let `-remote=auto` start it for you):

```sh
# Option A — auto-spawn on first use (simplest):
gopls -remote=auto cli def NewSession --in session.go
# Tip: alias 'gopls=command gopls -remote=auto' in your shell to drop the flag.

# Option B — long-lived daemon on a known socket:
gopls serve -listen "unix;/tmp/gopls.sock" -session.pool &
gopls -remote="unix;/tmp/gopls.sock" cli def NewSession --in session.go
# Pooled sessions are evicted after 15 min idle (override with -session.idle).
```

Every invocation starts a new CLI process that connects to the daemon,
runs the query, and exits. The daemon keeps the type-checked workspace
warm across invocations — the first query after daemon start is slow
(full Initial Workspace Load); subsequent queries are fast.

## Commands

**Navigate by symbol name (preferred — agents know names, not positions):**

```sh
gopls -remote=auto cli def  SYMBOL --in FILE           # go to definition
gopls -remote=auto cli refs SYMBOL --in FILE           # find all references
gopls -remote=auto cli hover SYMBOL --in FILE          # signature + docs
gopls -remote=auto cli impl SYMBOL --in FILE           # find implementations
gopls -remote=auto cli rename SYMBOL --in FILE --to NEWNAME
```

SYMBOL is a Go identifier. Use the bare name — receiver qualification is
handled automatically:
- Functions: `FindProjectRoot`, `main`
- Types: `Session`, `Cache`
- Methods: `Definition` (matches `(*Session).Definition` etc.)
- Fields: `IdleTimeout`
- Constants/variables: `maxMessageSize`, `MethodDefinition`

Qualified forms also work: `(*Session).Definition` or `Session.Definition`.
Use `Type.Method` form to narrow to a specific receiver when the plain
method name is ambiguous.

**Navigate by position (use when you already have FILE:LINE:COL, e.g.
parsing compiler output):**

```sh
gopls -remote=auto cli def  FILE:LINE:COL
gopls -remote=auto cli refs FILE:LINE:COL
gopls -remote=auto cli hover FILE:LINE:COL
gopls -remote=auto cli impl FILE:LINE:COL
gopls -remote=auto cli rename FILE:LINE:COL --to NEWNAME
```

**List and search symbols:**

```sh
gopls -remote=auto cli symbols FILE         # symbols defined in this file
gopls -remote=auto cli wsymbols QUERY       # fuzzy workspace-wide search
```

`symbols` output uses Go receiver syntax: `(*Session).FileOf`, `NewSession`.
`wsymbols` accepts fuzzy queries: `"Session"`, `"Session.FileOf"`,
`"FindProject"`.

**Diagnostics (compile errors, vet, analyzers):**

```sh
gopls -remote=auto cli check                   # workspace-wide
gopls -remote=auto cli check FILE [FILE...]    # per-file
gopls -remote=auto cli vet [FILE...]           # filtered to the cmd/vet suite
```

`check` returns every diagnostic gopls would surface in an editor —
compile errors, go/analysis findings, modernizers. `vet` is the same
pipeline filtered to the traditional `cmd/vet` analyzer set (printf,
copylock, structtag, …; see `settings.VetAnalyzerNames`). Output:

```
/path/session.go:39:6: error [compiler]: undefined: Context
/path/parser.go:12:2: warning [printf]: Printf format %d has arg x of wrong type string
```

Use `-json` for structured output.

**Format and organize imports:**

```sh
gopls -remote=auto cli format FILE [FILE...]       # reformat (gofmt + gopls rules)
gopls -remote=auto cli imports FILE [FILE...]      # organize imports
```

By default these print the edited content to stdout. Flags are
orthogonal — any combination works; if none is given, the default is
print-to-stdout:

- `-w` — write edited content back to the file in place
- `-d` — emit a unified diff
- `-l` — print the paths of files that would change

Example — format a file in place and show what changed:

```sh
gopls -remote=auto cli format -w -d ./foo.go
```

**Code actions (quickfix, refactor, source):**

```sh
gopls -remote=auto cli codeaction FILE:LINE:COL                   # list actions
gopls -remote=auto cli codeaction FILE:LINE:COL --kind quickfix   # filter by kind prefix
gopls -remote=auto cli fix       FILE:LINE:COL --kind quickfix    # apply one (must match exactly 1)
```

`codeaction` prints one line per action: `TITLE <TAB> KIND <TAB> [edit,command,resolvable]`
where the bracketed label is a comma-joined subset of `{edit, command, resolvable}`
(or `[-]` when none apply).
Use `--kind` to filter by the LSP CodeActionKind prefix
(`quickfix`, `refactor.inline`, `source.organizeImports`, etc.).

`fix` requires the filtered set to contain exactly one action. If
multiple match, it lists them and exits — refine `--kind` to
disambiguate. `fix` accepts the same `-w/-d/-l` edit-mode flags as
`format`/`imports`; default is print-to-stdout.

Commands (action.command without an edit) are not yet applied by `fix`;
those actions are surfaced by `codeaction` but ignored by `fix`.

## Position format

All positions use `FILE:LINE:COL` where:
- **LINE** is 1-based (matches compiler output)
- **COL** is 1-based UTF-8 byte column (matches `go/token.Position.Column`)

This matches what `go build`, `go vet`, and `grep -n` produce. Out-of-range
positions are clamped (helpful when agents estimate coordinates).

## Flags

- `-remote=auto` (global) — auto-spawn/connect to the daemon
- `-remote=unix;PATH` (global) — connect to a specific daemon socket
- `-json` (cli-flag) — emit JSON instead of terse text

Tip: alias `gopls='command gopls -remote=auto'` in your shell to drop the
`-remote` flag from every invocation.

## Output

Terse text, one result per line. Example — `def NewSession --in session.go`:

```
/path/to/session.go:39:6
```

Example — `hover NewSession --in session.go`:

```
func NewSession(ctx context.Context, c *Cache) *Session
NewSession creates a new gopls session with the given cache.
```

Use `-json` for programmatic consumption. Fields are stable.

## Rename preview

Rename returns a preview of the edits it would make — it does NOT modify
files. Review the output, then apply the changes yourself (or with a
separate tool).

```sh
gopls -remote=auto cli rename NewSession --in session.go --to CreateSession
# Output (text): per file, "PATH:" followed by indented
#   L:C-L:C → "newText"   lines, one per edit.
# Output (-json): raw protocol.WorkspaceEdit (documentChanges populated).
```

## When NOT to use

- Running tests → `go test`
- Building → `go build`
- Text search (comments, strings, non-identifier tokens) → `grep` / `rg`
- Reading file content → `cat` / Read tool
- Go documentation → `go doc`

Note: `gopls cli check` reports the same diagnostics as `go build` +
`go vet` + gopls's extra analyzers. Use the compiler directly if you
want raw build output; use `check` for the editor-equivalent view
(including modernizer suggestions and type-error-attached fixes).
`gopls cli format` / `imports` is a superset of `gofmt` / `goimports`
(applies gopls-specific transformations too).

## Troubleshooting

- **Connection refused** → daemon not running. Use `-remote=auto` or start
  one explicitly with `-listen -session.pool`.
- **Stale results after editing** → gopls auto-detects file changes on
  disk between connections. No manual sync command is needed.
- **Ambiguous symbol** → narrow with `Type.Method` form or fall back to
  `FILE:LINE:COL`.
- **First query is slow** → normal. The daemon is running IWL.
  Subsequent queries are fast.
- **Rename didn't update external test packages** → gopls rename covers
  same-module references. Verify with `refs` before and after.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (includes empty result for `def`, `refs`, `impl`, `symbols`, `wsymbols`) |
| 1 | Error (server failure, connection failure, or "no hover/rename target") |
| 2 | Bad arguments |

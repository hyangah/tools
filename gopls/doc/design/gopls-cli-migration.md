---
title: "gopls CLI migration plan"
---

## Status

**Draft.** Companion to [gopls-cli-prototype.md](gopls-cli-prototype.md)
and a concrete proposal for resolving
[golang/go#63693](https://github.com/golang/go/issues/63693) ("make a
decision about the gopls CLI").

This doc proposes the end-state CLI surface, explains how today's
two CLI surfaces (legacy top-level and the `gopls cli` prototype)
collapse into it, and sequences the work. The target reference —
sample help text for every end-state command — lives in
[gopls-cli-reference.md](gopls-cli-reference.md).

Two items are explicitly out of scope for this plan's MVP and
tracked separately: **interactive refactorings**
(`gopls.modify_tags`, `gopls.implement_interface` via
`command/resolve`) and the **configuration schema** for
`format`/`imports`/`check`/`vet`. Both are discussed briefly
below but deferred.

## Context: two CLI surfaces today

The gopls binary hosts commands under two paths:

- **Legacy top-level** (`gopls definition`, `gopls rename`,
  `gopls check`, ...): hand-use tools adopted over time by scripts
  and agents. Neither LSP-faithful nor agent-friendly.
- **`gopls cli` prototype** (`gopls cli def`, ...): agent-friendly
  defaults (symbol lookup, 1-based UTF-8, pull diagnostics, session
  pooling), but still a ~1:1 translation of LSP verbs under a `cli`
  prefix. See [gopls-cli-prototype.md](gopls-cli-prototype.md).

Neither serves agent and script use cases cleanly.

## End-state: two CLI surfaces, one binary

The binary hosts two CLI surfaces with disjoint purposes:

- **Top-level** commands (`gopls def`, `gopls rename`, ...) shaped
  around user/agent use cases, not LSP verbs. Stable, documented,
  agent-friendly. This is the surface agents and scripts should
  use.
- **`gopls cli <verb>`** — LSP methods exposed 1:1 as subcommands,
  for debugging, conformance checks, and reproducible invocation
  in bug reports. Best-effort output, not a supported integration
  point. See [Low-level LSP primitives under `gopls cli`](#low-level-lsp-primitives-under-gopls-cli).

### Design principles

- **Answer-shaped, not coordinate-shaped.** `gopls def --body`
  returns location + decl source; `gopls refs --context=N` includes
  surrounding source; `gopls hover` returns signature + doc.
  Follow-up reads the agent would otherwise make are folded in.
- **Use cases, not LSP verbs.** `gopls rename` validates and applies
  in one command (no separate `prepare-rename`); `gopls callers` /
  `gopls callees` wrap `prepareCallHierarchy` +
  `incomingCalls`/`outgoingCalls`.
- **Human/Agent-friendly defaults.** 1-based UTF-8 positions; clamping of
  out-of-range coordinates; pull diagnostics; session pooling; JSON
  via `--json`; terse text by default.
- **Bounded iteration: one pass.** `gopls check --fix` applies
  quickfixes in a single pass and reports remaining diagnostics;
  `gopls codeaction` applies one action at a position. If fixes
  introduce new diagnostics, the agent drives the loop.

### Proposed surface

| Command                                                        | Purpose                                                          |
|----------------------------------------------------------------|------------------------------------------------------------------|
| `gopls def SYMBOL --in FILE [--body]`                                   | Locate a symbol; optionally include decl source                  |
| `gopls refs SYMBOL --in FILE [--context=N]`                             | Find references; optionally with surrounding source              |
| `gopls impl SYMBOL --in FILE [--context=N]`                             | Find implementations                                             |
| `gopls hover SYMBOL --in FILE [--body]`                                 | Signature + doc comment; optionally full body                    |
| `gopls symbols FILE [--signatures]`                                     | Top-level decls; optionally with signatures                      |
| `gopls wsymbols QUERY`                                                  | Workspace symbol search                                          |
| `gopls callers SYMBOL --in FILE [--depth=N] [--context=N]`              | Incoming call graph                                              |
| `gopls callees SYMBOL --in FILE [--depth=N] [--context=N]`              | Outgoing call graph                                              |
| `gopls rename SYMBOL --in FILE --to NEW [-w\|-d\|-l\|--preserve\|--dry-run]` | Validate + apply rename                                         |
| `gopls check [FILE...] [--severity=S] [--fix] [-w\|-d\|-l\|--preserve]`    | Pull diagnostics; `--fix` applies quickfixes                     |
| `gopls vet [FILE...] [--severity=S] [--fix] [-w\|-d\|-l\|--preserve]`      | Filtered to cmd/vet analyzers                                    |
| `gopls format FILE... [-w\|-d\|-l\|--preserve]`                            | Apply gofmt                                                      |
| `gopls imports FILE... [-w\|-d\|-l\|--preserve]`                           | Organize imports                                                 |
| `gopls codeaction FILE:LINE:COL [--kind KIND] [-w\|-d\|-l\|--preserve]`    | List or apply a code action (quickfix, refactor, source.*)      |

Interactive refactorings (`gopls.modify_tags`,
`gopls.implement_interface`) are deferred to a follow-up; see
[Interactive refactoring on the CLI](#interactive-refactoring-on-the-cli).
Full help text in [gopls-cli-reference.md](gopls-cli-reference.md).

## Low-level LSP primitives under `gopls cli`

`gopls cli <verb>` exposes LSP methods 1:1 as CLI subcommands. It is
not a replacement for the top-level surface; it is a scriptable
debug path.

### Why keep a low-level surface

- **Skill-side LSP conformance.** Agent skills that depend on
  specific gopls responses can smoke-test them against a real server
  without wiring an LSP client.
- **Reproducible bug reports.** `gopls cli execute gopls.run_tests …`
  is a one-line repro; `-rpc.trace` + a harness is not.
- **Debugging without an editor.** Inspecting what gopls returns for
  `semanticTokens/full` or `foldingRange` on a given file currently
  requires either an editor plugin or a custom test binary.
- **Preservation, at near-zero cost.** Several commands are in the
  binary today (`semtok`, `execute`, `folding_range`, ...) and would
  otherwise be deleted with no replacement; keeping them namespaced
  under `cli` costs a dispatch entry each.

### Contents

| Subcommand                      | LSP method                         | Rationale                                             |
|---------------------------------|------------------------------------|-------------------------------------------------------|
| `gopls cli execute CMD [ARGS]`  | `workspace/executeCommand`         | Raw access to every `gopls.*` command verb            |
| `gopls cli semtok FILE`         | `textDocument/semanticTokens/full` | Debug semantic-token computation                      |
| `gopls cli links FILE`          | `textDocument/documentLink`        | Inspect document links; niche                         |
| `gopls cli codelens FILE`       | `textDocument/codeLens`            | Inspect per-file code lenses                          |
| `gopls cli folding-range FILE`  | `textDocument/foldingRange`        | Inspect fold regions                                  |
| `gopls cli highlight POS`       | `textDocument/documentHighlight`   | Raw view of a method whose agent use case is `refs`   |
| `gopls cli signature POS`       | `textDocument/signatureHelp`       | Raw view of a method whose agent use case is `hover`  |
| `gopls cli prepare-rename POS`  | `textDocument/prepareRename`       | Raw view of a method whose agent use case is `rename --dry-run` |

### Contract

- **1:1 with LSP.** Each subcommand is a thin dispatcher around a
  single LSP method. No answer-shape folding, no follow-up reads,
  no quality-of-life enrichment. Agents should use the top-level
  commands; `gopls cli` is for debugging.
- **Output.** `--json` emits the server response verbatim (as
  serialized by the protocol package). Text output is a minimal,
  best-effort pretty-print; not guaranteed stable.
- **Position convention.** 1-based UTF-8 bytes on input, matching
  the rest of the CLI. `--json` output preserves LSP-native
  coordinates (0-based UTF-16) as the spec requires; text output
  converts to 1-based UTF-8. Users who want pure LSP coordinates
  everywhere can read the JSON.
- **Stability.** `gopls cli <verb>` output is best-effort faithful
  to the LSP spec and the gopls server's implementation of it.
  Shapes may change as the spec evolves or as internal types are
  refactored. Not part of gopls' top-level CLI stability surface;
  not a supported integration point for agent skills.

### Primitives-only, not a full LSP mirror

`gopls cli` hosts only the primitives above — it is not a 1:1
mirror of every top-level command. No `gopls cli def`, `cli refs`,
`cli hover` returning raw LSP types. The top-level surface is the
one we commit to; duplicate entries add confusion ("which do I
use?") with no user we can name today. Revisit if skill authors
make the case for raw-LSP versions of existing top-level commands.

## Configuration

`format`, `imports`, `vet`, and `check` are only meaningfully
better than their external equivalents (`gofmt`, `goimports`,
`go vet`) when they apply the user's gopls settings. The current
CLI ignores most of them, so `gopls format` / `gopls imports`
degrade to `gofmt` / `goimports` with extra latency.

**Proposed direction**: a documented config schema, searched as
`./gopls.json` or `.gopls.json` (workspace) →
`$XDG_CONFIG_HOME/gopls/config.json` (user) → built-in defaults,
overridable via `--config PATH` or `GOPLS_CONFIG`. Editor plugins
are encouraged to read the same files; reusing editor-specific
configs directly is rejected as fragile. Piggybacking on
`go env GOPLSCONFIG` is a possible alternative.

Until config lands, `format` and `vet` are hard to justify over
their external counterparts — the schema is a post-Phase-1 follow-up
that graduates those commands from "equivalent with latency" to
"strictly better inside a workspace." Coordination with the gopls
team and editor-plugin owners required; tracked in
[Open questions](#open-questions).

## Migration

### Mapping from today to end-state

| Today                           | End-state                                                                                                   |
|---------------------------------|-------------------------------------------------------------------------------------------------------------|
| `gopls definition`              | Redesigned as `gopls def`; legacy shape deprecated one release, then deleted.                               |
| `gopls references`              | → `gopls refs`                                                                                              |
| `gopls implementation`          | → `gopls impl`                                                                                              |
| `gopls rename`                  | Redesigned (validate + apply + dry-run). Same name. Legacy flag shape preserved where compatible.           |
| `gopls check`                   | Redesigned: pull-diagnostic, severity filter preserved. Same name.                                          |
| `gopls format`, `gopls imports` | Same names; agent-friendly defaults.                                                                        |
| `gopls codeaction`              | Same name, same semantics; no rename. Listing and applying are one command: no `--kind` lists, `--kind K` applies. Covers quickfix, refactor, and `source.*` actions — not just fixes. |
| `gopls call_hierarchy`          | Split into `gopls callers` + `gopls callees`.                                                               |
| `gopls prepare_rename`          | Agent use case folded into `gopls rename --dry-run`; raw LSP view preserved as `gopls cli prepare-rename`.  |
| `gopls signature`               | Agent use case covered by `gopls hover`; raw LSP view preserved as `gopls cli signature`.                   |
| `gopls highlight`               | Agent use case covered by `gopls refs`; raw LSP view preserved as `gopls cli highlight`.                    |
| `gopls folding_range`           | → `gopls cli folding-range` (debug only; no agent use case at top-level).                                   |
| `gopls semtok`                  | → `gopls cli semtok` (debug only).                                                                          |
| `gopls links`                   | → `gopls cli links` (niche).                                                                                |
| `gopls codelens`                | → `gopls cli codelens`. Revisit a top-level command if agents want test-lens invocation.                    |
| `gopls execute`                 | → `gopls cli execute`. Raw `workspace/executeCommand` for debugging.                                        |
| `gopls vulncheck`               | Keep as-is (not code-intelligence).                                                                         |
| `gopls stats`, `remote`, `mcp`  | Keep as-is (admin / peer commands).                                                                         |
| `gopls cli <op>` (prototype)    | Prototype agent subcommands removed; namespace hosts low-level LSP primitives only (see [Low-level LSP primitives under `gopls cli`](#low-level-lsp-primitives-under-gopls-cli)). |

### Phased rollout

**Phase 0 — Fill in the remaining agent-utility additions under
`gopls cli`.** The prototype already carries a working baseline
(edit-flag-aware `rename`, `--dry-run`, `--severity`, `--preserve`,
`--body`). Three end-state commands in the [Proposed surface](#proposed-surface)
still need implementations:

- `callers` / `callees` — wrap `prepareCallHierarchy` +
  `incomingCalls` / `outgoingCalls`, with `--depth=N` and
  `--context=N`.
- `refs --context=N` — include surrounding source per reference.
- `symbols --signatures` — include each top-level decl's signature.

Each ships as its own CL under `cli`. The namespace is unannounced
with no public users, so iteration is free. Config loading and
interactive refactorings are separate follow-ups, not Phase 0 gates.

**Phase 1 — Promote to top-level, narrow `cli`.** Single cut-over.
Redesigned commands register at the top level; `gopls cli <op>` is
narrowed in the same CL — every current agent subcommand
(`def`, `refs`, `hover`, `rename`, `check`, ...) is removed, and
the low-level primitives listed in
[Low-level LSP primitives](#low-level-lsp-primitives-under-gopls-cli)
are wired in (via direct move for those already present as legacy
top-level commands, new implementation for any that are missing).
Legacy top-level commands that keep their name get the new
semantics; those with diverging names (e.g. `gopls definition` →
`gopls def`) emit a one-line deprecation notice and dispatch to the
new command. Legacy commands with no top-level replacement either
move under `gopls cli` per the migration table or are deleted
outright. Release notes announce the new surface, the `gopls cli`
scope, and the #63693 resolution. Exit: top-level agent surface +
`gopls cli` debug surface, both documented.

## Open questions

- **Configuration source of truth** ([Configuration](#configuration)).
  Direction proposed but not committed; requires coordination with
  the gopls team and editor-plugin owners. Post-Phase-1 follow-up,
  not a Phase 1 blocker — Phase 1 ships with `format`/`vet`
  equivalent to their external counterparts, and the schema work
  graduates them to "strictly better inside a workspace."
- **Interactive refactorings on the CLI.** Deferred to a follow-up
  (see [Interactive refactoring on the CLI](#interactive-refactoring-on-the-cli)).
  Open sub-questions for when that follow-up starts: protocol shape
  for the Q&A loop (prompts on stderr vs full TTY form); wire
  format for non-interactive inputs (stdin JSON vs `--answers FILE`
  vs per-field `--set KEY=VAL`); whether to share the client-side
  form logic with MCP or any other non-LSP surface.
- **Deprecation window for legacy aliases.** Full deprecation runs
  two gopls release cycles (~6 months), release-management's call
  on dates. During the window, an opt-in env var
  (e.g. `GOPLS_LEGACY_CLI=1`) selects legacy interpretation for
  subcommand names that collide between surfaces (`gopls rename`,
  `gopls check`, ...), so existing scripts keep working without
  code changes while users migrate. Needs confirmation that an env
  var is the right channel vs. a flag or detection heuristic.
- **Telemetry for legacy aliases.** Accepted: emit a counter on
  each deprecated-alias invocation so we have usage data before
  deletion. Counter names and retention are an implementation
  detail.
- **Stability contract for `gopls cli`.** Accepted as stated in
  [Contract](#contract). Operational question: land the `--help`
  caveat and docs as soon as the prototype narrowing starts, not
  at Phase 1 cut-over, so the window where users could pick up
  prototype-form subcommands assuming stability stays small.

## Interactive refactoring on the CLI

Deferred to a post-MVP follow-up. `command/resolve` (see
[integrating-interactive-refactoring.md](integrating-interactive-refactoring.md))
assumes a client that renders forms and retries, which the CLI does
not today provide.

Intended direction when this lands: the CLI acts as an extended LSP
client. It calls `command/resolve`, receives the server's
`formFields`, prompts the user on stdin/stderr for each field, and
sends back `formAnswers` to obtain the edit. Non-interactive
callers (agents, scripts) pipe structured answers via stdin or
`--answers FILE`. This avoids baking form fields into per-command
CLI flag schemas — the server remains the source of truth for the
schema, and the CLI has no hand-coded flag list to drift.

Scope-wise this means implementing client-side form/retry logic in
the CLI, which is a non-trivial addition and not required for the
#63693 resolution. Tracked in [Open questions](#open-questions).

## Out of scope

- Session pooling, watcher ownership, diagnostic cache — server-side
  and orthogonal; covered by the prototype doc.
- MCP server surface. Migrating MCP tool definitions to share the new
  top-level implementations is a separate effort this plan unblocks.
- Non-code-intelligence commands (`stats`, `remote`, `mcp`,
  `vulncheck`) — remain under their own rules.

---
title: "gopls CLI prototype"
---

## Status

**Prototype / experiment.** This document describes the design of the
`gopls cli` subcommand suite and the supporting server-side machinery
that makes CLI invocations fast (session pooling, pool-scoped file
watcher, pull-diagnostic cache, per-connection capability profiles).
It is shipped unannounced, behind flags, for evaluation and feedback.

The `gopls cli` commands duplicate functionality already present in the
legacy top-level commands (`gopls definition`, `gopls references`,
`gopls rename`, `gopls check`, `gopls format`, `gopls imports`,
`gopls vet`, `gopls codeaction`, `gopls fix`). Two command families
coexist only while we validate the agent-friendly shape. The end state
is a single CLI: the learnings here are expected to fold into the
legacy commands, either by merging `gopls cli X` into `gopls X` or by
deleting one side once the other covers the ground. See
[Migration plan](#migration-plan).

Tracking issue: [golang/go#63693](https://github.com/golang/go/issues/63693)
("make a decision about the gopls CLI").

## Goals

1. Provide code intelligence (definition, references, hover, symbols,
   implementations, rename, diagnostics, formatting, import
   organization, code actions, quick fixes) to non-interactive clients
   — shell scripts and AI coding agents — with sub-100ms warm-query
   latency.
2. Reuse gopls internals: the CLI is an LSP client that speaks the
   same protocol as editors. No new wire protocol.
3. Share state across clients: a gopls daemon can serve an editor, an
   MCP client, and many short-lived CLI invocations against the same
   workspace without reloading it.

## Non-goals

- Replace the editor protocol. This work does not modify the LSP
  protocol that editors negotiate.
- Provide a streaming / long-lived CLI session. Each `gopls cli`
  invocation is one short connection.
- Cover every LSP operation. The suite is scoped to the operations an
  agent needs to read and safely transform Go source.

## Background

Agents and shell scripts need type-aware code navigation. The legacy
`gopls definition` commands work, but each invocation creates a fresh
`cache.Session` and pays 1–30 seconds of Initial Workspace Load (IWL)
— even when connecting to a daemon via `-remote=auto`. The daemon
creates and destroys a session per LSP connection
(`internal/lsprpc/lsprpc.go`). Across tens of invocations per agent
task, this dominates latency.

Previous attempts (internal prototypes v2 and v3) introduced a custom
length-prefixed JSON protocol that bypassed the LSP session lifecycle.
Those attempts were rejected because a third wire format alongside LSP
and MCP is a long-term maintenance burden.

This prototype keeps the protocol standard and moves the optimization
server-side. The CLI is a normal LSP client; the daemon keeps the
heavy state (`cache.Session`, Views, type-check results, analysis
results, file watcher) alive across connections and hands it to each
new connection.

## Architecture

```
┌────────────────────────────────────────────────────────────────┐
│                      gopls daemon process                       │
│              (gopls serve -session.pool=true)                   │
│                                                                 │
│  ┌──────────┐     ┌──────────┐     ┌──────────────────────┐   │
│  │ LSP      │     │ MCP      │     │ LSP                  │   │
│  │ (editor) │     │ (agents) │     │ (gopls cli / legacy) │   │
│  └────┬─────┘     └────┬─────┘     └──────────┬───────────┘   │
│       │                │                      │                │
│       └────────────────┴──────────────────────┘                │
│                        │                                        │
│         ┌──────────────┴───────────────┐                       │
│         │        cache.Cache           │   Session pool        │
│         │  + pooled cache.Sessions     │   (keyed by root)     │
│         │    (Views, file watcher,     │   • acquire on        │
│         │     type-check results,      │     pool hit          │
│         │     diagnostic cache)        │   • register on miss  │
│         │                              │   • evict after idle  │
│         └──────────────────────────────┘                       │
└────────────────────────────────────────────────────────────────┘
                      ▲
                      │ Standard LSP over Unix socket / TCP
                      │ (-remote=auto, -remote=unix;path, ...)
                      │
             ┌────────┴─────────┐
             │  gopls cli def   │  ← short-lived client
             │  gopls cli refs  │     (agent or shell)
             └──────────────────┘
```

The CLI connects to the daemon as a regular LSP client. On connect,
the daemon either finds a warm session for the workspace root or
creates one. On disconnect, the session stays in the pool for the next
connection; idle sessions are evicted after a configurable timeout.

## Session pool

The pool lives in `internal/lsprpc` and is keyed by workspace root.

### Pool entry lifecycle

Each pool entry wraps a `cache.Session` plus pool-scoped state
(reference count, idle timer, shared file watcher, diagnostic cache,
push-subscriber set).

- **Acquire** — on LSP connect, the pool returns the entry for the
  requested root if one exists, incrementing its reference count.
- **Register** — on pool miss, after the new session finishes
  initialization with its Views, it is registered in the pool. Races
  are handled: if another connection registered first, the winner is
  used and the loser session is discarded.
- **Release** — on LSP disconnect, the reference count is decremented.
  When the count reaches zero, an idle timer starts.
- **Evict** — after the idle timeout, the pool closes the shared file
  watcher (before shutting down the session, to drain in-flight
  events), then shuts down the session and removes the entry.

Default idle timeout: 5 minutes. Overridable with `-session.idle`.

### Server integration: hooks

The session pool lives in `internal/lsprpc`; the per-connection
`*server.Server` lives in `internal/server`. To avoid a circular
import, the pool integrates via two optional hooks set on the server
at construction time:

- **`SessionSwapHook`** — fires at the start of `addFolders` (during
  the Initialize / Initialized handshake). When the hook returns a
  pooled session, the server swaps its temporary session for the
  warm one. The existing `HasView()` / `ErrViewExists` checks in
  `addFolders` naturally cause IWL to be skipped for folders whose
  Views already exist.
- **`PostInitHook`** — fires at the end of `addFolders`, after the
  Views have been created. On pool miss, this registers the newly
  warmed session into the pool. Separating pool-miss registration
  from the swap hook prevents a race where a second connection could
  observe a not-yet-initialized entry.
- **`onShutdown`** — optional callback that runs in place of
  `session.Shutdown()` during server shutdown. The pool installs this
  to release the session back to the pool (and start the idle timer)
  rather than destroy it.

The two-hook structure is an internal server concern and is not
observable from LSP clients.

## File watching across connections

On baseline gopls, each `*server.Server` owns its own fsnotify watcher
and tears it down on server shutdown. With session pooling, the
`cache.Session` outlives the server, and a file edited on disk between
two connections goes unobserved — the old watcher is gone, and no new
connection is there yet to see the event. The pooled snapshot becomes
stale without anyone noticing.

### Pool-scoped watcher

The file watcher is moved from `*server.Server` to the pool entry.
It is created lazily on the first connection to the entry
(`EnsureWatcher`), shared by any `*server.Server` attached to the
same session, and closed before session shutdown during pool
eviction. Its `onChange` closure captures the `*cache.Session` (not
a `*server.Server`), so events continue to invalidate the snapshot
after a connection shuts down.

`WatchDir` is idempotent across connections via a pool-scoped
`watchedDirs` set — each new connection re-requests the same
directories during its own `updateServerSideWatcher` pass, and the
pool deduplicates.

Watcher mode (`fsnotify` vs `off`) is sticky for the pool entry's
lifetime: the first-creator wins. Dynamic mode changes via
`workspace/didChangeConfiguration` do not propagate to the pool-owned
watcher. This is acceptable for the prototype.

### Push-diagnostic fan-out

A disk edit observed by the pool-scoped watcher invalidates the
session snapshot, but the editor attached to the pooled daemon still
needs a `publishDiagnostics` to see the change reflected in its UI.

The pool entry maintains a set of push-diagnostic subscribers. Each
`*server.Server` whose client wants push diagnostics registers a
callback at `addFolders`. When a watcher event fires, the pool entry:

1. Calls `session.DidModifyFiles` on the modifications.
2. Iterates the subscriber set and calls each callback. The callback
   on the `*server.Server` publishes `publishDiagnostics` for the
   modified URIs and (if the client also has push diagnostics enabled)
   kicks off a background `diagnoseChangedViews` goroutine.

Multi-client topology is handled by the shared diagnostic cache (see
[Shared diagnostic cache](#shared-diagnostic-cache)): the first
goroutine across all attached servers pays the type-check and
analysis cost; the rest publish from cache.

## Diagnostics for short-lived clients

A short-lived CLI connection cannot rely on push diagnostics: the
client disconnects before the server's asynchronous
`diagnoseSnapshot` completes. The prototype supports three cooperating
mechanisms.

### Pull diagnostics

The server implements LSP 3.17 pull diagnostics:

- `textDocument/diagnostic` — synchronous per-URI request-response.
- `workspace/diagnostic` — synchronous workspace-wide request-response.

Both are enabled when the client advertises pull-diagnostic
capabilities and sets `pullDiagnostics: true` in
`initializationOptions`. The `gopls cli check`, `cli vet`,
`cli codeaction`, and `cli fix` subcommands opt into this via a
capability profile (see [CLI capability profile](#cli-capability-profile)).

`textDocument/diagnostic` existed before this work as a per-file
handler; it has been extended to read from the shared diagnostic
cache. `workspace/diagnostic` was a `notImplemented` stub before this
work; it now iterates the session's views and emits one
`WorkspaceFullDocumentDiagnosticReport` per compiled Go file, with
URI dedup.

Current scope limits:
- Full reports only (no `resultId` / `Unchanged` incremental reports).
- No partial-result streaming.
- Go source files only (not `go.mod`, `go.work`, templates).

### Push-subscriber gate

With short-lived, pull-only CLI connections attached to a pooled
session, the server should not run push-model diagnostic computation
— no client will receive the results and the work is wasted.

The pool entry counts push subscribers. Each connection registers as
a push subscriber exactly once during `addFolders` if its client
capabilities include push diagnostics; the count is decremented in
`Shutdown`. `(*server).shouldComputeDiagnostics()` (defined in
`internal/server/text_synchronization.go`) gates the
modification-triggered workspace diagnose pass — when pooling is on
and no connection wants push diagnostics, `diagnoseChangedViews` is
not scheduled. CLI connections declare `wantsPushDiagnostics=false`
in their init options and therefore never count.

A re-entry guard on the subscribe side (`s.poolSubscribed`) ensures
each server subscribes exactly once even when `addFolders` is
re-entered via `DidChangeWorkspaceFolders` or a `DidOpen` fallback,
so `Shutdown`'s single `Unsubscribe` keeps the count balanced.

In parallel, the CLI's `skipDidOpen` profile (see
[CLI capability profile](#cli-capability-profile)) avoids the
redundant `DidOpen`/modification chatter entirely, eliminating the
view invalidation that pool-hit CLI reads used to cause.

### Shared diagnostic cache

Per-snapshot diagnostic results live on the pool entry as a
`DiagnosticCache` keyed by `(URI, *cache.View)`. Both the push path
(`updateAndPublish`, `publishFileDiagnosticsLocked`,
`findMatchingDiagnostics`) and the pull path (`Diagnostic`,
`DiagnosticWorkspace`) go through the cache.

Freshness rule: a new entry wins if it is for a newer snapshot, or
for the same snapshot and marked `final=true`.

The `final` flag distinguishes the fast pass (type-check only, no
analysis) from the full pass (type-check plus `go/analysis`). When
`settings.DiagnosticsDelay > 0`, `diagnoseSnapshot` stores a fast-pass
entry and then a full-pass entry. Pull requires `final=true`, so a
pull arriving inside the delay window does not silently drop analyzer
diagnostics (staticcheck, etc.).

Per-connection state (`publishedHash`, `mustPublish`, `orphanedAt`)
remains per-`*server.Server`: those fields describe what a given
server has fanned out on its own client wire. Only the heavy compute
results are shared.

Lock order: `server.diagnosticsMu` (outer) → `DiagnosticCache.mu`
(inner, transient). Cache methods are self-contained and never
re-enter server code.

## CLI capability profile

Each outgoing LSP connection from the `gopls` CLI selects a
`clientProfile` before `app.connect()`. The profile controls:

- `wantsPushDiagnostics` (sent in `initializationOptions`) — whether
  this connection registers as a push subscriber on the pool entry
  and receives `publishDiagnostics` notifications.
- `WorkDoneProgress` client capability — off for short-lived CLI
  connections (progress is noise).
- `pullDiagnostics` client capability and init option — on for the
  subcommands that consume diagnostics.
- `skipDidOpen` — whether to send `textDocument/didOpen` before query
  methods. Off for all `gopls cli` subcommands: CLI clients have no
  unsaved buffers, disk is authoritative, and the pool-scoped file
  watcher keeps the snapshot current across connections. The server's
  query handlers read files via `snapshot.ReadFile`, which falls back
  to disk when no overlay exists.

The profile is selected per-subcommand in `cmd.cliProfileFor`:

| Subcommand                                | Push diag | Pull diag | DidOpen | Edits out     |
|-------------------------------------------|:---------:|:---------:|:-------:|:--------------|
| def, refs, hover, impl, symbols, wsymbols |    off    |    off    |  skip   | —             |
| rename                                    |    off    |    off    |  skip   | WorkspaceEdit |
| format, imports                           |    off    |    off    |  skip   | TextEdits     |
| check, vet                                |    off    |    on     |  skip   | —             |
| codeaction, fix                           |    off    |    on     |  skip   | WorkspaceEdit |

The default profile (used by editor LSP clients and the legacy
top-level commands) keeps the existing behavior: push diagnostics on,
`WorkDoneProgress` on, no pull-diagnostic advertisement.

## `gopls cli` subcommand suite

The CLI subcommands are grouped under `gopls cli` and dispatched in
`gopls/internal/goplscli/cmd/cli.go`. They share:

- **Symbol-based lookup.** `def NewSession --in session.go` resolves
  the symbol name to a position via `textDocument/documentSymbol` and
  walks the tree (handling Go's `(T).Name` method naming) to find a
  match. Agents know names, not coordinates.
- **Position model.** CLI positions are 1-based line, 1-based UTF-8
  byte column (matching `go/token.Position` and compiler output).
  LSP positions are 0-based UTF-16. Conversion happens at the CLI
  boundary. Out-of-range positions from hallucinated coordinates are
  clamped to the nearest valid position rather than rejected.
- **Terse output.** Default text mode prints one
  `file:line:col[:symbol]` per line. `-json` emits a structured JSON
  document.
- **`params.Range` compatibility.** gopls's query methods accept
  `params.Range`. The CLI sets both fields.

### Read-only queries

`def`, `refs`, `hover`, `impl`, `symbols`, `wsymbols`. All use the
base CLI profile (no push, no progress, skip DidOpen).

`def` and `hover` accept `--body`, which folds in the source of the
declaration (located via `textDocument/documentSymbol` on the def
file). Removes a follow-up file Read for the common agent flow of
"jump to def, then look at the code." Falls back to empty body when
no symbol matches the def position (e.g., for non-top-level
definitions); the command still exits 0.

### Edit-producing query: rename

`gopls cli rename` returns a `WorkspaceEdit` that the CLI applies via
the same `applyWorkspaceEdit` machinery as `cli fix`. It accepts the
same `EditFlags` as `format`/`imports` (`-w` / `-d` / `-l` /
`--preserve`), with the default being print-edited-content-to-stdout.

`--dry-run` prints a terse per-file summary of the proposed edits and
exits without modifying any files. Useful for review before applying.
The summary printer iterates `DocumentChanges` first and falls back
to `Changes`; gopls only ever writes to `DocumentChanges`, but
servers in general populate either.

### Diagnostics: check, vet

`gopls cli check [FILE...]`:
- No args → one `workspace/diagnostic` request, reports for every Go
  file in the workspace in one round-trip.
- Args → one `textDocument/diagnostic` request per file.
- Output: `file:line:col: severity [source]: message`, sorted.

`--severity=LEVEL` (`error`, `warning`, `info`, `hint`,
case-insensitive) filters out diagnostics less severe than LEVEL.
Matches the legacy `gopls check -severity` semantics.

`gopls cli vet [FILE...]` is a source-filtered view of `check`:
identical wire behavior, but filters returned diagnostics to those
whose `Diagnostic.Source` matches an analyzer in the traditional
`cmd/vet` suite. Membership is tracked by a new `inVet` field on
`settings.Analyzer`; `settings.VetAnalyzerNames()` returns the set.
A drift test fails if the `inVet` classification diverges from
`go tool vet help`. `vet` also accepts `--severity`.

### Edits: format, imports

`gopls cli format FILE...` and `gopls cli imports FILE...` produce
edits via `textDocument/formatting` and
`textDocument/codeAction` (with `Only=[SourceOrganizeImports]`).
They share the legacy `EditFlags` pattern:

| Flag            | Behavior                                      |
|-----------------|-----------------------------------------------|
| (default)       | Print edited content to stdout                |
| `-w, --write`   | Overwrite file in place (only if changed)     |
| `-d, --diff`    | Print unified diff                            |
| `-l, --list`    | Print filename only if file would change      |
| `--preserve`    | With `-w`, copy the original to `<file>.orig` |
| `--json`        | Per-file `[{file, changed, newContent}, ...]` |

The dispatcher routes format/imports out of the standard
result→print pipeline because the edit flags control side effects
that don't fit a single encoded result.

### Code actions: codeaction, fix

`gopls cli codeaction FILE:LINE:COL [--kind KIND]` lists available
code actions at a position. It pulls `textDocument/diagnostic` first,
filters diagnostics to those whose range covers the cursor, and
passes them in `CodeActionParams.Context.Diagnostics`. The server's
`codeActionsMatchingDiagnostics` path requires this field — without
pulling diagnostics first, quickfix actions would not appear.
`--kind` maps to `Context.Only` for server-side filtering.

`gopls cli fix FILE:LINE:COL [--kind KIND] [-w|-d|-l]` picks one
matching action and applies its `WorkspaceEdit`. If `action.Edit` is
nil and `action.Data` is non-nil, `codeAction/resolve` is called
first. Actions that require `workspace/applyEdit` round-trips are
reported as unsupported — in-prototype scope is the inline-edit path.
Edit application walks each `TextDocumentEdit` in
`WorkspaceEdit.DocumentChanges`; file renames and creates are not yet
supported.

## Daemon lifecycle and flags

### `-session.pool`

`gopls serve -session.pool` enables session pooling on the daemon.
Without this flag the daemon behaves as before: one `cache.Session`
per LSP connection, destroyed on disconnect.

### `-session.idle`

Idle timeout for pooled sessions. Default 5 minutes. Short enough
that warm sessions from one-off CLI invocations get reclaimed
promptly; users who want longer retention can raise it.

### `-remote=auto` auto-spawn

Without configuration, `gopls -remote=auto cli <cmd>` would fail if
no daemon were running — the auto-spawn path was never taken. The
`connect()` helper now passes a `daemonArgs` callback to
`ConnectToRemote` that builds the spawn argv with `-session.pool`
and `-logfile=auto`. Users get the pool without having to remember
the flag.

### Client-side fast path for `-remote=<addr>`

`main.go` calls `filecache.Get("nonesuch", ...)` at startup as a
defensive ENOSPC / cache-corruption check (golang/go#67433). The
check costs ~21ms per invocation. CLI processes invoked with
`-remote=<addr>` forward to a daemon and never touch the local
filecache; the daemon performs the same check at its own startup.
The client-side call is gated off in remote mode. Measured impact:
~16–19ms saved per warm CLI invocation.

## Performance

Measured on x/tools (~800 packages, GoWork view), Apple Silicon,
Go 1.26.x. Each query is a separate CLI process connecting over a
Unix socket. 11 runs per command, true median, one warm-up discarded.
"v3" is the rejected custom-protocol prototype, retained as a
latency baseline.

### Cold start (first query, includes IWL)

| Version  | Time (ms) |
|----------|-----------|
| Prototype | 972–1166  |
| v3        | 1406–1829 |
| Legacy    | 2046      |

### Warm queries — `gopls cli`

| Command   | Prototype (ms) | v3 (ms) |
|-----------|---------------:|--------:|
| def       | 70             | 40      |
| refs      | 78             | 42      |
| hover     | 69             | 40      |
| symbols   | 69             | 40      |
| wsymbols  | 77             | 54      |
| impl      | 72             | 41      |

### Warm queries — legacy commands (`gopls -remote=auto <cmd>`)

Session pooling applies to any LSP connection, not just `gopls cli`.
The existing top-level commands benefit without any client changes.

| Command        | Prototype (ms) | Without pool (ms) | Speedup |
|----------------|---------------:|------------------:|--------:|
| definition     | 70             | 1956              | 28x     |
| references     | 99             | 2236              | 23x     |
| symbols        | 75             | 715               | 10x     |
| workspace_sym  | 86             | 1974              | 23x     |
| implementation | 82             | 1869              | 23x    |

### Per-query overhead

The prototype pays ~28ms of LSP handshake overhead per connection
(Initialize + Initialized + RegisterCapability + Shutdown
round-trips) on top of ~15–20ms of Go process startup. v3 paid ~5ms
(one custom-protocol round-trip). The prototype is ~28–30ms slower
per warm query than v3; the tradeoff is that there is no third wire
protocol to maintain.

## Migration plan

The `gopls cli` suite and the legacy `gopls <operation>` commands
currently duplicate each other. The path to a single user-facing
surface is the subject of a separate doc — see
[gopls-cli-migration.md](gopls-cli-migration.md), which proposes an
end-state where top-level commands (`gopls def`, `gopls rename`, ...)
host the agent-friendly surface and `gopls cli <verb>` narrows to
LSP primitives for debugging.

The session-pooling work under `internal/lsprpc` and the capability
profile under `internal/cmd` are not affected by that choice — they
serve any LSP client equally.

Until the migration lands, the `gopls cli` subcommands are
undocumented on the public site, behind the implicit "experimental"
label that covers unannounced CLI subcommands.

## Follow-up work

The prototype has landed end-to-end; this section tracks work we deferred
on purpose so reviewers know what's in scope for later iterations.

### Open decisions

- **Migration plan.** Proposed in
  [gopls-cli-migration.md](gopls-cli-migration.md); resolution blocks
  the public announcement and user-facing docs.
- **Persistent CLI connection** (§[Alternatives considered](#alternatives-considered),
  option C). Revisit if the ~28ms per-query handshake shows up in
  user reports.
- **Pool key = root only** (§[Alternatives considered](#alternatives-considered),
  option D). Add build-config (`GOOS`, tags, …) to the pool key if we
  see clients with divergent configs sharing workspaces.

### Scope limits to lift

Each of these is called out in the prototype as a deliberate cut;
none are required for correctness but lifting them expands coverage.

- **Pull diagnostics: incremental reports.** Add `resultId` /
  `Unchanged` support on `textDocument/diagnostic` and
  `workspace/diagnostic` so push-capable editors can also benefit from
  the shared diagnostic cache without recomputing.
- **Pull diagnostics: partial-result streaming.** Stream
  `workspace/diagnostic` results as views finish rather than holding
  until the full pass completes.
- **Pull diagnostics: non-Go files.** Extend `workspace/diagnostic`
  coverage to `go.mod`, `go.work`, and template files — matching the
  push path.
- **Dynamic watcher-mode changes.** A `workspace/didChangeConfiguration`
  that flips `fileWatcher` currently logs a warning but is ignored by
  the pool-owned watcher. Either re-create the watcher on mode change
  or document the restriction in user-facing settings.
- **`gopls cli fix`: `workspace/applyEdit` round-trips.** Actions that
  require the server to drive the edit (not return it inline) are
  reported as unsupported. Wire up a client-side `ApplyEdit` handler so
  these actions work end-to-end.
- **`gopls cli fix`: file renames and creates.** Edit application walks
  `TextDocumentEdit` entries only. Extend to `RenameFile` and
  `CreateFile` in `WorkspaceEdit.DocumentChanges`.

### Test coverage

- **Eviction race.** Cover the window where the idle timer fires
  concurrently with a new `acquire` for the same key.
- **Multi-client pool sharing.** Verify that two concurrent connections
  to the same root (e.g. editor + CLI, or CLI + CLI) land on the same
  pool entry and share the diagnostic cache and pool-scoped watcher.

### Cleanup

- **Scrub internal-only references in code comments.** Several comments
  reference internal design artifacts (`kb-gopls-skills/…`,
  `research/FILE_WATCHER_AUDIT.md`) and staged-development labels
  (`Stage 1`, `Stage 3c`, …) that aren't meaningful to readers of the
  upstream tree. Replace with pointers to this design doc and
  self-contained explanations.

## Alternatives considered

### A. Custom wire protocol (v2, v3)

A length-prefixed JSON protocol with stateless one-shot connections.
~5ms per-query overhead. Rejected twice by the gopls team because a
third wire format alongside LSP and MCP is a long-term maintenance
burden. The prototype pays ~28ms instead and avoids the protocol
debt.

### B. In-process library (no daemon)

Each agent process links gopls internals directly. Zero IPC. Rejected
because N agents × 500–800 MB resident. No state or cache sharing.

### C. Persistent CLI connection

Keep the CLI connected to the daemon across queries, amortizing the
LSP handshake over a run. Rejected for the prototype because it adds
connection-management complexity and the per-query overhead has not
proven to be a problem in practice. Worth revisiting if user reports
show the handshake is the bottleneck.

### D. Pool keyed by full config

The pool is keyed on workspace root only. Two clients with different
`GOOS` or build tags share a pool entry and get whichever
configuration arrived first. Acceptable because agent workflows
almost never customize the build config; adding config to the pool
key would multiply resident sessions. Future work if it matters.

## Appendix: source map

| Component                      | Path                                                           |
|--------------------------------|----------------------------------------------------------------|
| Session pool                   | `gopls/internal/lsprpc/pool.go`                                |
| Pool / StreamServer wiring     | `gopls/internal/lsprpc/lsprpc.go`                              |
| Server hooks                   | `gopls/internal/server/server.go`, `general.go`                |
| Push-subscriber compute gate   | `gopls/internal/server/text_synchronization.go`                |
| Diagnostic cache               | `gopls/internal/server/diagnostics.go`                         |
| `workspace/diagnostic`         | `gopls/internal/server/diagnostics.go`                         |
| Client profile, `initParams`   | `gopls/internal/cmd/cmd.go`, `cli.go`                          |
| `gopls serve` flags            | `gopls/internal/cmd/serve.go`                                  |
| `-remote=auto` auto-spawn      | `gopls/internal/cmd/cmd.go` (daemonArgs)                       |
| Filecache smoke-test skip      | `gopls/main.go`                                                |
| CLI subcommand dispatch        | `gopls/internal/goplscli/cmd/cli.go`                           |
| CLI rename                     | `gopls/internal/goplscli/cmd/rename.go`                        |
| CLI `--body` fetch             | `gopls/internal/goplscli/cmd/body.go`                          |
| CLI check/vet                  | `gopls/internal/goplscli/cmd/check.go`                         |
| CLI format/imports             | `gopls/internal/goplscli/cmd/format.go`                        |
| CLI codeaction/fix             | `gopls/internal/goplscli/cmd/codeaction.go`                    |
| Position conversion            | `gopls/internal/goplscli/position.go`                          |
| Symbol resolution              | `gopls/internal/goplscli/resolve.go`                           |
| `inVet` classification         | `gopls/internal/settings/analysis.go`                          |

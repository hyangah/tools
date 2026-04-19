# gopls cli v4: Design

## Summary

`gopls cli` is a shell-based interface to gopls's Go code intelligence,
designed for AI coding agents. It exposes go-to-definition, references,
hover, rename, symbol search, diagnostics (check/vet), formatting,
import organization, and single-shot code-action application through
simple CLI commands.

The CLI connects to the gopls daemon via standard LSP using
`-remote=auto`. When the daemon has session pooling enabled
(`-session.pool`), subsequent connections reuse a warm session instead
of paying the full Initial Workspace Load (IWL) cost.

## Motivation

AI coding agents need type-aware code navigation but cannot speak LSP
interactively. The legacy `gopls definition` commands work but create a
fresh gopls server per invocation, paying 1-30 seconds of IWL every
time — even when connecting to a daemon via `-remote=auto`. The
bottleneck is not the protocol but the session lifecycle: the daemon
creates and destroys a `cache.Session` per connection
(`lsprpc.go:88-138`).

Previous designs (v2: custom length-prefixed JSON; v3: custom protocol
routed through protocol.Server) were rejected by the gopls team because
maintaining a third wire protocol alongside LSP and MCP is a long-term
burden.

v4 eliminates the custom protocol entirely. The CLI speaks standard LSP
to the daemon. Performance comes from server-side session pooling, not
protocol optimization.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  gopls daemon process               │
│           (gopls serve -session.pool)               │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌───────────────────┐ │
│  │ LSP      │  │ MCP      │  │ LSP               │ │
│  │ (editor) │  │ (agents) │  │ (gopls cli / any) │ │
│  └────┬─────┘  └────┬─────┘  └────┬──────────────┘ │
│       │              │             │ same protocol   │
│       │              │             │                 │
│       └──────────────┴─────────────┘                │
│                      │                               │
│              ┌───────┴────────┐  Session Pool        │
│              │  cache.Cache   │  (keyed by root)     │
│              │  + pooled      │  - acquire on connect│
│              │    Sessions    │  - release on close  │
│              │                │  - evict after idle  │
│              └────────────────┘                      │
└─────────────────────────────────────────────────────┘
         ▲
         │ Standard LSP over Unix socket / TCP
         │ (via -remote=auto or -remote=unix;path)
         │
┌────────┴────────┐
│  gopls cli def  │  ← agent or human
│  gopls cli refs │
└─────────────────┘
```

### Key design decisions

**Standard LSP, no custom protocol.** The CLI connects to the daemon as
a regular LSP client, going through the full Initialize/Initialized
handshake. This eliminates the maintenance cost of a custom wire
protocol. The tradeoff is ~28ms of per-query overhead for the LSP
handshake (vs ~5ms for v3's custom protocol).

**Session pooling for performance.** The `sessionPool` in
`lsprpc.StreamServer` keeps `cache.Session` instances alive across
connections, keyed by workspace root. On pool hit, `addFolders` finds
existing Views via `HasView()` and skips IWL entirely. On pool miss
(first connection), the session is created normally and registered in
the pool after initialization.

**Two-hook integration.** A `SessionSwapHook` fires at the start of
`addFolders` to swap the temporary session for a pooled one (pool hit).
A `PostInitHook` fires at the end of `addFolders` to register the
newly-initialized session in the pool (pool miss). This avoids circular
imports between `lsprpc` and `server` packages.

**Agent-friendly CLI wrapper.** `gopls cli` adds symbol-based lookup
(`def Parse --in parser.go`), terse output (one `file:line:col` per
line), and JSON output (`-json`). It wraps the standard LSP operations
with position conversion. File opening is optional: since Stage 4b,
CLI clients skip `textDocument/didOpen` and let the daemon read target
URIs from disk (see "Capability profile" below).

**Capability profile.** Each `gopls cli` subcommand picks a per-request
client capability profile before the LSP Initialize handshake. This is
selected by `cliProfileFor(args[0])` in `internal/cmd/cmd.go`, which
branches on the subcommand name:

| Subcommand                                | Push diag | Pull diag | DidOpen | Edits out    |
|-------------------------------------------|-----------|-----------|---------|--------------|
| def, refs, hover, impl, symbols, wsymbols | —         | —         | skip    | —            |
| rename                                    | —         | —         | skip    | WorkspaceEdit|
| format, imports                           | —         | —         | skip    | TextEdits    |
| check, vet                                | —         | pull      | skip    | —            |
| codeaction, fix                           | —         | pull      | skip    | WorkspaceEdit|

All profiles drop push `publishDiagnostics` (CLI doesn't display
squiggles) and `workDoneProgress` (short-lived connections make
progress noise), and skip `textDocument/didOpen` (CLI has no unsaved
buffers). Diagnostic-consuming subcommands additionally advertise LSP
3.17 pull-diagnostic client capabilities and set
`pullDiagnostics=true` in `initializationOptions` so the server
exposes `textDocument/diagnostic` and `workspace/diagnostic`.

The profile is a plain field (`Application.profile`) set in
`cliCmd.Run` before `app.connect`; nothing forces it to be static.
See `gopls/internal/cmd/cmd.go` and the `How this landed` section in
`kb-gopls-skills/v4/CAPABILITY_DRIVEN_PROPOSAL.md` for the full wiring.

**Shared cache.Cache.** The CLI sessions share `cache.Cache` with
editor LSP and MCP listeners. Editor-warmed caches (memoizedFS,
filecache, modCache) benefit CLI queries.

**Symbol-based queries.** Agents know symbol names, not positions. The
`--in FILE` syntax resolves a symbol name to a position via
`textDocument/documentSymbol`, handling Go's method naming convention
where methods appear as `(T).Name`.

**Position model.** CLI positions are 1-based line, 1-based UTF-8 byte
column (matching `go/token.Position` and Go compiler output). LSP
positions are 0-based line, 0-based UTF-16 character offset. Conversion
happens at the CLI boundary. `CLIToProtocol` clamps out-of-range
positions to the nearest valid position, since AI agents may hallucinate
coordinates.

**Note on params.Range.** gopls uses `params.Range` (not
`params.Position`) for all query methods including definition,
references, hover, and implementation. The CLI sets both fields for
compatibility.

## Package structure

```
gopls/internal/goplscli/
├── cmd/
│   ├── cli.go              Subcommand dispatch + read-only handlers
│   │                         (def/refs/hover/impl/symbols/wsymbols/rename)
│   ├── check.go            check/vet handlers (workspace/document pull diagnostics)
│   ├── format.go           format/imports handlers (-w/-d/-l edit flags)
│   ├── codeaction.go       codeaction/fix handlers (--kind filter, edit application)
│   └── benchmark_test.go   In-process performance benchmark
├── position.go             UTF-8 (CLI) ↔ UTF-16 (LSP) position conversion
└── resolve.go              Symbol name → position resolution

gopls/internal/lsprpc/
├── pool.go                 Session pool (acquire/register/release/evict)
├── pool_test.go            Unit tests
└── pool_integration_test.go Integration tests with real LSP

gopls/internal/server/
├── server.go               SessionSwapHook, PostInitHook, onShutdown
├── general.go              Hook integration in addFolders and Shutdown
└── diagnostics.go          DiagnosticWorkspace (LSP 3.17 pull, Stage 3e)

gopls/internal/cmd/
├── cli.go                  cliCmd subcommand + cliServer fallback wrapper
├── cmd.go                  clientProfile + cliProfileFor + initParams
├── profile_test.go         Profile/init-params regression pins
└── serve.go                -session.pool flag

gopls/internal/settings/
└── analysis.go             inVet field + VetAnalyzerNames() for `cli vet` filter
```

## Data flow

A typical warm query (`gopls -remote=auto cli def NewSession --in session.go`):

1. CLI parses args into method="definition", symbol="NewSession", file.
2. `app.connect(ctx)` connects to the daemon via `-remote=auto`.
   The daemon's `ServeStream` creates a temporary `cache.Session`.
3. `client.initialize()` sends Initialize + Initialized.
   - `addFolders` fires `SessionSwapHook` → pool hit → swaps to warm
     session. Temporary session is discarded (no Views).
   - `HasView()` returns true for all folders → no IWL.
   - `nsnapshots.Wait()` returns immediately.
4. Subcommand dispatcher in `goplscli/cmd.Run` calls
   `server.Definition()` directly — no `didOpen` is sent because the
   CLI profile advertises `skipDidOpen: true`. The daemon reads the
   target URI from disk (or the pooled overlay) as needed.
5. Symbol resolution: `ResolveSymbol()` calls `textDocument/documentSymbol`,
   walks the symbol tree, returns the matching position.
6. `server.Definition()` returns `[]protocol.Location`.
7. Positions are converted from 0-based UTF-16 to 1-based UTF-8.
8. CLI prints `"/abs/path/session.go:39:6"` (text) or structured JSON.
9. Connection closes. `server.Shutdown()` calls `onShutdown()` which
   releases the pooled session back to the pool (not destroyed).

## Performance

Measured on the x/tools repo (~800 packages, GoWork view), Apple M-series,
Go 1.26.2. Each query is a separate CLI process connecting to the daemon
over a Unix socket. 11 runs per command, true median reported, one
warm-up call discarded before timing. See `BENCHMARK-v2.md` in
`kb-gopls-skills/v4/` for raw output and full methodology.

### Cold start (first query, includes IWL)

| Version | Time (ms) |
|---------|-----------|
| v4      | 972–1166  |
| v3      | 1406–1829 |
| Legacy  | 2046      |

Ranges reflect two runs with opposite ordering (v3-first, v4-first) to
control for OS cache state. v4 is consistently faster than v3 on cold
start in both orderings; the gap narrows when v3 runs second with a
warmed cache but direction is unchanged.

### Warm queries — `gopls cli` subcommand

| Command   | v4 (ms) | v3 (ms) | v4 − v3 (ms) |
|-----------|---------|---------|--------------|
| def       | 70      | 40      | +30          |
| def (sym) | 66      | —       | —            |
| refs      | 78      | 42      | +36          |
| hover     | 69      | 40      | +29          |
| symbols   | 69      | 40      | +29          |
| wsymbols  | 77      | 54      | +23          |
| impl      | 72      | 41      | +31          |

### Warm queries — legacy commands (`gopls -remote=... <cmd>`)

v3's legacy path has no session pool — every invocation pays full IWL.
v4's session pool benefits legacy commands equally, since `-session.pool`
applies to all LSP connections, not just `gopls cli`.

| Command        | v4 (ms) | v3 (ms) | v4 speedup |
|----------------|---------|---------|------------|
| definition     | 70      | 1956    | 28x        |
| references     | 99      | 2236    | 23x        |
| symbols        | 75      | 715     | 10x        |
| workspace_sym  | 86      | 1974    | 23x        |
| implementation | 82      | 1869    | 23x        |

This is arguably the most practically significant result: v4 makes the
existing `gopls -remote` subcommands usable for scripting without any
client-side changes.

### Overhead analysis

Per-query overhead of v4 vs v3 (pool-hit path, warm):
- **Process startup**: ~15-20ms (Go binary startup, equal to v3)
- **LSP ceremony**: ~28ms (Initialize + Initialized + RegisterCapability
  + Shutdown round-trips vs v3's single round-trip)
- **Total delta**: ~28–30ms per warm query

See `OVERHEAD_ANALYSIS.md` for the round-trip-count decomposition.

### Multi-query session comparison

| Scenario              | Legacy | v3      | v4      |
|-----------------------|--------|---------|---------|
| 1 query (cold)        | ~2.0s  | ~1.4–1.8s | ~1.0–1.2s |
| 1 query (warm)        | ~2.0s  | ~40ms   | ~70ms   |
| 5-query session       | ~10s   | ~0.2s   | ~0.3s   |

### The tradeoff

v4 is ~1.75x slower per warm query than v3 (~28ms extra). In exchange:
- No custom protocol to maintain (v3 had ~2,050 lines of server-side
  custom wire protocol)
- Standard LSP compatibility — any LSP client benefits from session
  pooling, not just `gopls cli`
- The gopls team does not need to review/maintain a third wire format

## Session pool details

### Pool key

```go
type poolKey struct {
    root       string  // workspace root path
    configHash string  // empty for MVP; future: hash of build config
}
```

### Lifecycle

- **Acquire**: `pool.acquire(key)` returns a warm session if one exists,
  incrementing its reference count.
- **Register**: `pool.register(key, session)` adds a newly-initialized
  session. Handles races (if another connection registered first, returns
  the winner).
- **Release**: `pool.release(key)` decrements reference count. When zero,
  starts an idle timer.
- **Evict**: After the idle timeout (default 15 minutes), the session is
  shut down and removed from the pool.

### Shutdown interception

When a `server.Server` using a pooled session receives `Shutdown()`, it
calls `onShutdown()` instead of `session.Shutdown()`. The `onShutdown`
callback releases the session back to the pool. The session stays alive
for the next connection.

## Diagnostics

Push diagnostics don't work for short-lived CLI clients. The CLI
instead uses pull diagnostics (LSP 3.17):

- `textDocument/diagnostic` — per-URI; used by `cli check FILE...`,
  `cli vet FILE...`, `cli codeaction`, and `cli fix`.
- `workspace/diagnostic` — workspace-wide (Stage 3e); used by bare
  `cli check` / `cli vet`.

Both are enabled by advertising `textDocument.diagnostic` /
`workspace.diagnostics` client capabilities plus
`pullDiagnostics: true` in `initializationOptions`. This is done per
subcommand via `cliPullProfile` in `internal/cmd/cmd.go`.

`cli vet` is a source-filtered view of `cli check`: it reads the same
reports and keeps only diagnostics whose `Source` field is the name of
an analyzer in the traditional `cmd/vet` suite (`settings.inVet = true`,
exposed via `settings.VetAnalyzerNames()`). Out-of-tree compile errors
still appear from the type-checker, but modernizer/styling hints are
filtered out.

`cli codeaction` requests `textDocument/diagnostic` first to populate
`CodeActionParams.Context.Diagnostics` with diagnostics that cover the
requested position, then calls `textDocument/codeAction`. `cli fix`
wraps that with single-action selection and `WorkspaceEdit` application.

## Related issue

[golang/go#63693](https://github.com/golang/go/issues/63693): "make a
decision about the gopls CLI." Session pooling makes the existing CLI
commands performant enough for interactive and agent use, strengthening
the case for promoting the CLI from "experimental" to stable.

## Limitations

- **LSP handshake per query**: ~28ms overhead per connection for the
  Initialize/Initialized ceremony. Could be reduced by a persistent
  connection mode in the future.
- **No file-change detection between connections**: The session's
  snapshot is frozen at disconnect. Future: file-change scan on
  reconnect (~10-50ms).
- **Single workspace root pooling**: MVP pools by root path only.
  Different build configurations (GOOS, build tags) are not
  distinguished.

## Alternatives considered

### A. Custom wire protocol (v2/v3)

A length-prefixed JSON protocol with stateless one-shot connections.
~5ms per-query overhead (no LSP handshake).

Pro: Lower per-query overhead.
Con: Third wire protocol to maintain alongside LSP and MCP. Rejected by
the gopls team twice.

### B. In-process library (no daemon)

Each agent process links gopls internals directly.

Pro: Zero IPC. Direct function calls.
Con: N agents × 500-800 MB. No memory sharing.

### C. Persistent CLI connection

Keep the CLI connected to the daemon across queries, amortizing the
LSP handshake over multiple queries.

Pro: Eliminates the ~28ms per-query handshake overhead.
Con: Adds connection management complexity. May be worth exploring if
the per-query overhead proves problematic in practice.

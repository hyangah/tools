# gopls cli: Design Overview

## Summary

`gopls cli` is a shell-based interface to gopls's Go code intelligence,
designed for AI coding agents. It exposes go-to-definition, references,
hover, rename, diagnostics, and symbol search through simple CLI commands
over a Unix socket, bypassing the LSP protocol layer entirely.

## Motivation

AI coding agents need type-aware code navigation (definition, references,
rename) but cannot speak LSP. The alternatives are:

1. **grep/ripgrep** — text-only, no type awareness, misses cross-package
   references, can't rename safely.
2. **go/packages + go/types** — correct but requires re-analysis on every
   query (~seconds per invocation).
3. **Subprocess LSP** — full protocol overhead: JSON-RPC framing,
   initialize/shutdown handshake, content-length headers, text document
   sync, capability negotiation. Each query requires ~10 protocol round
   trips.

`gopls cli` provides option (4): **in-process access to gopls analysis
functions**, wrapped in a trivial wire protocol. One Unix socket
connection per query. No LSP ceremony.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  gopls daemon process               │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌───────────────────┐ │
│  │ LSP      │  │ MCP      │  │ CLI Server        │ │
│  │ (editor) │  │ (agents) │  │ (gopls cli)       │ │
│  └────┬─────┘  └────┬─────┘  └────┬──────────────┘ │
│       │              │             │                 │
│       └──────────────┴─────────────┘                 │
│                      │                               │
│              ┌───────┴────────┐                      │
│              │  cache.Cache   │  ← shared            │
│              │  (memoization) │    across all three   │
│              └───────┬────────┘                      │
│                      │                               │
│              ┌───────┴────────┐                      │
│              │ cache.Session  │  ← per workspace     │
│              │   → Snapshot   │    root               │
│              │   → golang.*   │  ← analysis funcs    │
│              └────────────────┘                      │
└─────────────────────────────────────────────────────┘
         ▲
         │ Unix socket (length-prefixed JSON)
         │
┌────────┴────────┐
│  gopls cli def  │  ← agent or human
│  gopls cli refs │
│  ...            │
└─────────────────┘
```

### Key design choices

**In-process, not LSP.** The CLI server calls `golang.Definition`,
`golang.References`, etc. directly — the same functions that gopls's LSP
server uses, but without the `server.go` dispatch layer, protocol
encoding, or JSON-RPC framing. This eliminates ~5,000 lines of protocol
machinery per query.

**Shared cache.** The CLI server, LSP server, and MCP server share one
`cache.Cache` instance. When an editor has already loaded a workspace,
CLI queries hit warm caches — definition lookup takes ~2µs instead of
~100ms cold start. No extra memory for a second gopls process.

**Session pool.** `CLIHandler` maintains one `GoSession` per workspace
root, keyed by the directory containing `go.mod` or `go.work`. Sessions
are created on first access and evicted after 15 minutes of inactivity.
This avoids re-initializing the Go environment (`go env -json`, ~117ms)
on every query while cleaning up stale sessions automatically.

**Trivial wire protocol.** Length-prefixed JSON: 4-byte big-endian
length + JSON payload. One connection per request. No handshake, no
capabilities, no session state. This makes the protocol trivially
implementable in any language and debuggable with `nc`.

**Symbol-based queries.** Agents know symbol names, not positions. The
`--in FILE` syntax resolves a name to a position via `DocumentSymbols`,
handling Go's method naming convention (`(*Type).Method`). This avoids
a round trip to look up line:col before every query.

## Package structure

```
gopls/internal/goplscli/
├── cmd/
│   └── cli.go          CLI subcommand parser & output formatters
├── autostart.go        Daemon auto-start & socket path derivation
├── handler.go          CLIHandler: session pool with idle eviction
├── session.go          GoSession: wraps cache.Session, file sync
├── serve.go            Wire server, dispatch, request handlers
├── protocol.go         Wire types (Request, Response, CLILocation, ...)
├── position.go         UTF-8 (CLI) ↔ UTF-16 (protocol) conversion
└── resolve.go          Symbol name → position resolution
```

## Data flow

A typical query (`gopls cli def GoSession --in session.go`):

```
1. CLI parses args           → Request{Method:"definition", Symbol:"GoSession",
                                       File:"/abs/path/session.go"}

2. CLI connects to daemon    → Unix socket dial (auto-start if needed)

3. Wire framing              → [4 bytes: length][JSON payload]

4. Daemon dispatch           → serve.go:dispatch() routes to handleLocations()

5. Session lookup            → handler.SessionForFile(file)
                              → FindProjectRoot(file)  // walks up for go.mod
                              → handler.SessionFor(root) // pool lookup or create

6. File sync                 → session.EnsureSynced(file)
                              → mtime+size fingerprint check (skip if unchanged)
                              → session.DidModifyFiles() if changed

7. Symbol resolution         → resolve.ResolveSymbol(session, file, "GoSession", 0)
                              → DocumentSymbols tree walk
                              → matchesSymbolName() handles "(T).Name" suffix

8. Analysis                  → session.Definition(ctx, uri, rng)
                              → session.FileOf(ctx, uri) → snapshot + filehandle
                              → golang.Definition(ctx, snapshot, fh, rng)

9. Position conversion       → ProtocolToCLI(mapper, pos)
                              → 0-based UTF-16 → 1-based UTF-8 byte column

10. Response                 → CLILocation{File, Start{Line,Col}, End{Line,Col}}
                              → JSON marshal → length-prefixed write → close conn

11. CLI output               → "/abs/path/session.go:26:6" (text)
                              or [{"File":"...", "Start":{"Line":26,"Column":6}}] (JSON)
```

## Position model

gopls internals use 0-based line, 0-based UTF-16 character offset
(LSP standard). CLI uses 1-based line, 1-based UTF-8 byte column
(matching `go build`, `go vet`, `grep -n`, and `go/token.Position`).

Conversion happens at the handler boundary using `protocol.Mapper`:
- **CLI → protocol:** `CLIToProtocol(mapper, CLIPosition)` — clamps
  out-of-range positions to EOF/EOL instead of erroring.
- **Protocol → CLI:** `ProtocolToCLI(mapper, protocol.Position)` — uses
  `mapper.OffsetLineCol8()` for byte column conversion.

## File synchronization

gopls operates on in-memory file overlays, not disk. The CLI must tell
the daemon about file changes before querying:

- **`EnsureSynced(file)`** — called automatically before every query.
  Uses mtime+size fingerprinting to skip unchanged files. Holds a mutex
  across read+modify to prevent TOCTOU races.
- **`ForceSync(file)`** — explicit sync via `gopls cli sync FILE`.
  Stats the file before reading to ensure the fingerprint matches the
  content actually sent.
- **Open-before-change invariant:** `session.DidModifyFiles` requires
  `file.Open` before `file.Change`. GoSession tracks open state per file.

## Session management

`CLIHandler` is the session pool:

- **Workspace root discovery:** `FindProjectRoot()` walks up from the
  file looking for `go.work` (highest priority), then `go.mod`. Falls
  back to the file's directory for GOPATH/ad-hoc mode. Stops at the
  user's home directory.
- **GoEnv caching:** `go env -json` costs ~117ms. Results are cached
  per workspace root to avoid repeated subprocess calls.
- **Double-check locking:** `SessionFor()` checks the map, releases the
  lock to create the session, then re-checks before inserting.
- **Idle eviction:** Background goroutine ticks every 60s, evicts
  sessions idle for >15 minutes (configurable).
- **Shutdown:** `Close()` uses `sync.Once` on the done channel, stops
  the evictor, shuts down all sessions.

## Daemon integration

The CLI server is added to the gopls `serve` command as a parallel
listener alongside LSP and MCP:

```go
// gopls/internal/cmd/serve.go
sharedCache := cache.New(nil)

group.Go(func() error { /* LSP server using sharedCache */ })
group.Go(func() error { /* MCP server using sharedCache */ })
group.Go(func() error {
    return goplscli.Serve(ctx, s.CLIAddress, sharedCache)
})
```

The `--cli.listen` flag specifies the Unix socket address. Auto-start
derives a deterministic path from `debug.ReadBuildInfo()` so multiple
gopls versions don't collide.

## Comparison with gopls legacy commands

gopls already has CLI subcommands (`gopls definition`, `gopls references`,
`gopls rename`, etc.) that work via LSP. `gopls cli` is designed
specifically for agents and automation. The key differences:

| | `gopls <command>` (legacy) | `gopls cli <command>` (new) |
|---|---|---|
| **Protocol** | Full LSP over JSON-RPC | Length-prefixed JSON over Unix socket |
| **Startup** | New LSP session per invocation (~100ms+) | Shared daemon, warm pool (~2µs) |
| **Input** | Position only (`FILE:LINE:COL`) | Symbol name (`SYMBOL --in FILE`) or position |
| **Output** | Human-oriented (full file content, diffs) | Structured (`FILE:LINE:COL` text, or `--json`) |
| **Machine-readable** | No `--json` flag | `--json` on all commands |
| **Session reuse** | None — initialize/shutdown each time | Pool per workspace root, 15min idle eviction |
| **Cache sharing** | Separate from editor | Shares `cache.Cache` with editor's gopls |
| **Rename** | Can apply with `-w` | Dry-run only (prints edits for agent to apply) |

**Practical differences for agents:**

1. **Symbol-based input.** `gopls rename` requires `FILE:LINE:COL`,
   so the agent must first resolve the symbol's position. `gopls cli`
   accepts `SYMBOL --in FILE` directly.

2. **JSON output.** Legacy commands print human-readable text (full
   file contents, unified diffs). `gopls cli --json` returns structured
   data with `File`, `Line`, `Column` fields.

3. **Session reuse.** Legacy commands start a fresh LSP session per
   invocation. `gopls cli` reuses a warm session pool backed by the
   same cache the editor populated. From `benchmark_test.go`: cold
   definition is ~102ms, warm definition is ~1.7µs, wire round trip
   is ~53µs (Apple M2).

4. **Simpler protocol.** Legacy commands perform LSP initialize,
   textDocument/didOpen, the request, and shutdown (~10 messages per
   query). `gopls cli` is one request, one response, close.

5. **Dry-run rename.** `gopls cli rename` prints edits without applying
   them. The agent reviews and applies. `gopls rename -w` applies
   directly, which is useful for humans but risky for automated use.

## Limitations and future work

- **No automatic file watching.** Agents must call `sync` after edits.
  Per-query sibling file detection is designed but not yet implemented.
- **Single file sync.** Syncing a whole package requires multiple calls.
- **Rename is dry-run only.** The CLI prints edits; the agent applies
  them. This is intentional — agents need to review before applying.
- **External test packages.** Rename may not cover `_test` packages
  that use an external package name.
- **Unix sockets only.** No TCP or Windows named pipes yet.
- **No completion.** Code completion is not exposed. It requires a
  different interaction model (streaming candidates, filtering).

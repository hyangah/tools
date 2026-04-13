# gopls cli: Design

## Summary

`gopls cli` is a shell-based interface to gopls's Go code intelligence,
designed for AI coding agents. It exposes go-to-definition, references,
hover, rename, diagnostics, and symbol search through simple CLI commands.

A short-lived CLI process connects to the gopls daemon over a Unix
socket, sends one request, receives one response, and exits. The daemon
holds LSP server state across invocations, so each query is fast.

## Motivation

AI coding agents need type-aware code navigation but cannot speak LSP.
grep/ripgrep has no type awareness; go/packages requires re-analysis per
invocation (~seconds); subprocess LSP has ~10 messages of protocol
overhead per query. `gopls cli` provides in-process access to the gopls
LSP server, wrapped in a trivial wire protocol. One connection per query.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  gopls daemon process               │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌───────────────────┐ │
│  │ LSP      │  │ MCP      │  │ CLI Server        │ │
│  │ (editor) │  │ (agents) │  │ (gopls cli)       │ │
│  └────┬─────┘  └────┬─────┘  └────┬──────────────┘ │
│       │              │             │ ServerSession   │
│       │              │             │ wraps           │
│       └──────────────┴─────────────┘ protocol.Server│
│                      │                               │
│              ┌───────┴────────┐                      │
│              │  cache.Cache   │  ← shared            │
│              │  (memoized     │    across all three   │
│              │   analysis)    │                      │
│              └────────────────┘                      │
└─────────────────────────────────────────────────────┘
         ▲
         │ Unix socket (length-prefixed JSON)
         │
┌────────┴────────┐
│  gopls cli def  │  ← agent or human
│  gopls cli refs │
└─────────────────┘
```

### Key design decisions

**Routes through protocol.Server.** `ServerSession` wraps an in-process
`protocol.Server`, calling `server.Definition()`, `server.References()`,
etc. -- the same code path as editor LSP requests. File sync uses
`server.DidOpen()`/`server.DidChange()`. A minimal `cliClient` implements
`protocol.Client`, discarding most push notifications but caching
diagnostics.

**Simple wire protocol, not LSP.** The CLI is short-lived (one process
per invocation), so LSP's persistent stateful connections are
impractical. The wire protocol is length-prefixed JSON (4-byte
big-endian length + JSON payload), one request per connection, one
response, close.

**Session pooling.** `CLIHandler` pools `ServerSession` instances keyed
by workspace root. Each contains an initialized `protocol.Server` with
its own `cache.Session`, overlay tracking, and file fingerprints.
Sessions are evicted after 15 minutes of inactivity.

**Shared cache.Cache.** The CLI server shares `cache.Cache` with the
LSP and MCP listeners in the same gopls daemon. Editor-warmed caches
benefit CLI queries.

**File sync via didOpen/didChange.** `EnsureSynced()` uses mtime+size
fingerprinting to skip unchanged files; `ForceSync()` re-reads
unconditionally. The session tracks per-file open state for the
open-before-change invariant required by `DidChange`.

**Symbol-based queries.** Agents know symbol names, not positions. The
`--in FILE` syntax resolves a symbol name to a position via
`DocumentSymbols`, handling Go's method naming convention where methods
appear as `(T).Name`. This avoids a preliminary position lookup before
every query.

**Position model.** CLI positions are 1-based line, 1-based UTF-8 byte
column (matching `go/token.Position` and Go compiler output). LSP
positions are 0-based line, 0-based UTF-16 character offset. Conversion
happens at the handler boundary. `CLIToProtocol` clamps out-of-range
positions to the nearest valid position rather than erroring, since AI
agents may hallucinate coordinates.

## Package structure

```
gopls/internal/goplscli/
├── cmd/
│   └── cli.go          CLI subcommand parser & output formatters
├── autostart.go        Daemon auto-start & socket path derivation
├── handler.go          CLIHandler: session pool with idle eviction
├── session.go          ServerSession: wraps protocol.Server, file sync
├── serve.go            Wire server, dispatch, request handlers
├── protocol.go         Wire types (Request, Response, CLILocation, ...)
├── position.go         UTF-8 (CLI) ↔ UTF-16 (LSP) position conversion
└── resolve.go          Symbol name → position resolution
```

## Data flow

A typical query (`gopls cli def GoSession --in session.go`):

1. CLI parses args into `Request{Method:"definition", Symbol:"GoSession",
   File:"/abs/path/session.go"}`, connects to daemon (auto-start if
   needed), sends length-prefixed JSON.
2. Daemon `dispatch()` routes to `handleLocations()`.
3. `SessionForFile()` finds workspace root (walks up for
   `go.work`/`go.mod`), looks up or creates a `ServerSession`.
4. `EnsureSynced()` checks mtime+size; sends `didOpen`/`didChange` to
   the in-process server if the file changed.
5. `ResolveSymbol()` walks the `DocumentSymbols` tree, returns the
   symbol's `SelectionRange`.
6. `gs.Definition()` calls `server.Definition()` -- same code path as
   an editor's go-to-definition.
7. Positions are converted from 0-based UTF-16 to 1-based UTF-8,
   response is framed and sent, connection closes.
8. CLI prints `"/abs/path/session.go:26:6"` (text) or structured JSON.

## Wire protocol summary

Each connection carries exactly one request and one response. Framing is
4-byte big-endian length prefix followed by a JSON payload. Maximum
message size is 10 MB.

**Request** fields: `method` (string), `file` (absolute path), `line`
and `column` (1-based, for position queries), `symbol` (for name-based
queries), `query` (for workspace symbol search), `newName` (for rename),
`includeDeclaration` (for references), `dir` (workspace hint for
`wsymbols`).

**Response** fields: `error` (string, if failed), `locations` (for
definition/references/implementation), `hover`, `symbols`,
`workspaceSymbols`, `diagnostics`, `renameEdits`.

Methods: `definition`, `references`, `hover`, `implementation`,
`symbols`, `wsymbols`, `rename`, `diagnostics`, `sync`.

## Daemon lifecycle

The CLI derives a deterministic socket path from `debug.ReadBuildInfo`,
so multiple gopls versions use separate sockets. If the daemon is not
running, the CLI spawns `gopls serve` with `--listen` and `--cli.listen`
flags as a detached background process and retries the connection.

`CLIHandler` pools `ServerSession` instances keyed by workspace root.
`FindProjectRoot()` walks up from the file looking for `go.work` then
`go.mod` (falls back to the file's directory for GOPATH mode, stops at
`$HOME`). `SessionFor()` uses double-check locking: check map, release
lock to create session (LSP initialize handshake), re-check before
inserting. A background goroutine evicts sessions idle for >15 minutes.

## Performance

Measured on the x/tools repo (~803 packages, GoWork view), Apple M-series,
Go 1.26.2.

**Cold start** (first query to a fresh daemon, includes IWL): ~1.2s median.

**Warm queries** (daemon session already initialized):

| Command     | v3 warm (ms) | Legacy CLI (ms) | Speedup |
|-------------|-------------|-----------------|---------|
| definition  | 43          | 2046            | 48x     |
| references  | 77          | 2199            | 29x     |
| hover       | 40          | N/A             | —       |
| symbols     | 72          | 745             | 10x     |
| wsymbols    | 96          | 849             | 9x      |
| impl        | 43          | 2042            | 47x     |
| diagnostics | 98          | 1846            | 19x     |

The legacy CLI spawns a fresh gopls server per invocation, paying full
IWL + type-checking (~2s) every time. `gopls cli` connects to a persistent
daemon — after the first query pays initialization, all subsequent queries
complete in 40–100ms. A 5-query agent session takes ~0.3s with `gopls cli`
vs ~10s with the legacy CLI.

## Limitations

- **No automatic file watching.** Agents must call `sync` after edits,
  or rely on per-query `EnsureSynced` for the queried file.
- **Single file sync.** Syncing a whole package requires multiple calls.
- **Rename is dry-run only.** The CLI prints edits; the agent applies them.
- **Unix sockets only.** No TCP or Windows named pipes.

---

## Alternatives considered

### A. Direct golang.* calls, bypassing the LSP server

CLI handlers call `golang.Definition()`, `golang.References()`, etc.
directly, bypassing `protocol.Server`. A `GoSession` wrapper holds a
`cache.Session` and syncs files via `session.DidModifyFiles` instead of
LSP `didOpen`/`didChange`.

Pro: ~53us per request vs ~2-5ms through the server dispatch layer.
Con: Creates a parallel handler stack -- separate dispatch, session
management, and file sync -- alongside the LSP server.

Rejected: maintaining two code paths to the same `golang.*` functions is
a long-term burden. The ~2ms difference is irrelevant for AI agent
workloads where LLM inference dominates latency.

### B. External LSP broker with subprocess gopls

A separate broker daemon speaks the custom wire protocol to the CLI and
full LSP to a gopls subprocess. The broker manages LSP lifecycle on
behalf of short-lived CLI invocations.

Pro: language-agnostic -- could proxy for gopls, tsserver, pyright, etc.
Con: three processes (cli -> broker -> gopls), six serialization
boundaries per request, subprocess crash recovery, ~2500 lines of
broker/LSP-client code.

Rejected: Go-only scope eliminates the need for language-agnostic
abstraction. In-process access (same binary, shared `cache.Cache`) is
simpler and faster with no subprocess management.

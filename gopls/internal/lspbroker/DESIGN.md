---
title: "Gopls: LSP Broker"
---

This document describes the design and structure of the LSP broker, a
subsystem of gopls that exposes language server functionality through a
command-line interface. It is intended for contributors working on the
broker code; for user-facing documentation, see the CLI's `--help`
output.

## Motivation

AI coding agents need code intelligence — go-to-definition,
find-references, hover, diagnostics — and the Language Server Protocol
provides all of it. But agents that operate through shell commands cannot
speak LSP directly. Each agent harness that wants LSP
builds its own client, and each client spawns its own language server.
When many concurrent agent sessions each start their own language
server, the machine runs out of memory (see
[#19517](https://github.com/anthropics/claude-code/issues/19517) for
a representative report).

The broker solves both problems: it manages LSP server subprocesses
behind a long-lived daemon and exposes their functionality through a
thin CLI that any agent can call via shell. One daemon per user, one
language server per (project, language), shared across all agents.

## Architecture

The system has three layers:

```
Agent (any harness with shell access)
  └── calls `gopls lspcli <cmd>` via shell
         │
         │  unix socket / JSON-RPC
         ▼
      lspbrokerd (long-lived daemon)
      ├── routes requests to per-project sessions
      ├── manages file sync state
      └── collects diagnostics
         │
         │  stdio JSON-RPC (standard LSP)
         ▼
      Language servers (gopls, tsserver, rust-analyzer, …)
```

Both `lspcli` and `lspbrokerd` are subcommands of the `gopls` binary.
There are no separate binaries to install. When a CLI invocation finds
no running daemon, it spawns one automatically.

**Request flow.** The CLI connects to the daemon over a unix socket,
performs a version handshake, sends a single JSON-RPC request, reads
the response, formats it, and exits. The daemon looks up (or creates)
the appropriate session for the file in the request, resolves any
name-based symbol lookup into a position, forwards the LSP call to the
language server, and returns the result.

**Session routing.** Every request carries a file path. The daemon
walks up from that path looking for a project root: `.lsp.json`,
`go.work`, `go.mod`, `.git`, `.hg`, in that priority order. The
project root and the server ID (derived from the file extension and
configuration) together form the session key. Sessions are created
lazily on first access.

**File sync.** Before forwarding an LSP request, the broker ensures
the target file is open in the language server and its content matches
what is on disk. File state is tracked by mtime and size; redundant
syncs are skipped. Agents that edit files between LSP calls can use
`gopls lspcli sync FILE` to force a re-read.

**Diagnostics.** Language servers push diagnostics asynchronously via
`textDocument/publishDiagnostics`. The broker collects these in a
shared store. `gopls lspcli diagnostics FILE` polls the store with
short retries, returning whatever diagnostics have arrived.

## Design decisions

**Name-based lookup.** Agents think in names, not positions. The
primary CLI shape is `gopls lspcli def Parse --in parser.go`, not
`gopls lspcli def --in parser.go:42:7`. The broker resolves the name
by calling `textDocument/documentSymbol` on the target file, matching
the symbol name against the returned tree, and dispatching the
underlying LSP operation against the resolved position. The positional
form (`--in file:line:col` with no symbol argument) remains available
as an escape hatch. `--in` is always required, which eliminates
ambiguity about which project session to use.

**UTF-8 byte columns.** Positions on the CLI wire use 1-based lines
and 1-based UTF-8 byte columns, matching `go/token.Position.Column`,
`grep -bn`, and Go compiler error output. When the underlying language
server negotiates UTF-16, the broker's LSP client layer transcodes at
the boundary. Output uses the same convention, so a command's output
round-trips directly as input to a follow-up command:
`gopls lspcli refs --in $(gopls lspcli def Parse --in parser.go)`.

**Shared daemon.** A single daemon per user serves all agents and all
workspaces. This directly addresses the N × memory problem: ten agents
working on the same Go project share one gopls process instead of
spawning ten. Sessions within the daemon are keyed by project root, so
distinct projects get distinct language server instances.

**Version isolation.** Each gopls build gets a cache directory keyed
by its build ID (a hash of `go tool buildid` output). A new gopls
binary lands on a different socket and spawns a fresh daemon; the old
daemon idles out on its own timer. A handshake RPC on every new
connection and a protocol version field on every request provide
additional safeguards against version mismatch.

**Trust model.** A `.lsp.json` file can specify arbitrary commands to
run as language servers. The broker maintains an allowlist of trusted
project roots in `$XDG_CONFIG_HOME/lsp-broker/trusted.json`. Projects
with an untrusted `.lsp.json` are rejected. Go auto-detection via
`go.mod` does not consult `.lsp.json` and skips the trust check
entirely — it only spawns `gopls`, which is already on `$PATH`.

## Package layout

```
lspbroker/              broker core
├── cmd/                subcommand entrypoints (lspcli, lspbrokerd)
│   └── subcommands/    per-operation handlers (def, refs, hover, …)
├── lspclient/          generic LSP client over stdio
├── goadapter/          Go-specific session (spawns gopls subprocess)
└── format/             output formatters for CLI
```

The broker core contains the daemon (`Broker`), session management
(`Session`, `GenericSession`), the broker protocol types, project root
detection, `.lsp.json` configuration loading, file sync, diagnostics
collection, idle timeout, PID file management, trust enforcement, and
workspace edit application (for rename).

[lspclient] is a standalone LSP client library that speaks JSON-RPC
over stdio pipes to a language server subprocess. It handles the LSP
initialize handshake, position encoding negotiation, file open/change
lifecycle, and request dispatch. It is language-agnostic.

[goadapter] wraps lspclient with Go-specific behavior: it spawns a
`gopls serve` subprocess in the project root and registers a
diagnostics callback.

[format] converts LSP response types into human-readable text
(`file:line:col` locations, markdown hover blocks, symbol outlines)
and optional `--json` output.

## Configuration

Go projects are detected automatically by the presence of `go.mod` or
`go.work` and require no configuration. Other languages are configured
with a `.lsp.json` file at the project root:

```json
{
  "version": 1,
  "servers": {
    "typescript": {
      "command": ["typescript-language-server", "--stdio"],
      "extensionToLanguage": {".ts": "typescript", ".tsx": "typescriptreact"}
    }
  }
}
```

Each server entry specifies the command to run, a mapping from file
extensions to LSP language IDs, and optional fields for environment
variables, initialization options, settings, startup timeout, and
maximum restart count. Variable expansion (`${WORKSPACE_ROOT}`,
`${HOME}`, environment variables) is supported in command arguments and
environment values. Configuration is cached with mtime-based
invalidation; stale sessions are evicted when the config changes on
disk.

[lspclient]: https://pkg.go.dev/golang.org/x/tools/gopls@master/internal/lspbroker/lspclient
[goadapter]: https://pkg.go.dev/golang.org/x/tools/gopls@master/internal/lspbroker/goadapter
[format]: https://pkg.go.dev/golang.org/x/tools/gopls@master/internal/lspbroker/format

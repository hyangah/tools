# LSP Broker — Code Intelligence via Shell

`gopls lspcli` gives you code intelligence (go-to-definition, find-references, hover, rename, diagnostics, and more) for any language with an LSP server. It works through shell commands — no MCP client, no editor plugin, no LSP protocol knowledge required.

## Quick start

```sh
# Build (requires Go 1.23+):
cd path/to/golang.org/x/tools && go install ./gopls

# Go projects work out of the box — no config needed:
gopls lspcli def Parse --in ./parser.go
gopls lspcli refs Serve --in ./server.go
gopls lspcli hover Handler --in ./handler.go

# Other languages need a .lsp.json config (see "Multi-language" below).
```

The CLI auto-spawns a background daemon (`gopls lspbrokerd`) on first use. The daemon manages LSP server sessions, tracks open files, and caches diagnostics. You don't need to manage it — it starts automatically and exits after 30 minutes of inactivity.

## When to use this

Use `gopls lspcli` when you need to:

- **Navigate code:** find where a function/type/variable is defined, find all references, find implementations of an interface.
- **Understand code:** get type signatures, documentation, and hover info for any symbol.
- **Rename symbols:** safely rename a function/type/variable across all files that reference it.
- **Check diagnostics:** see compiler errors and linter warnings without running a full build.
- **Sync files:** tell the LSP server about files you just edited so queries return fresh results.

**Don't use it when:**
- You need to run tests, build, or execute code — use `go test`, `go build`, etc. directly.
- You need to format code — use `gofmt`, `prettier`, etc. directly.
- You need full-file content — use `cat` or your file-read tool.
- You're doing a text search — use `grep` or `rg`.

## Command reference

### Code navigation

All navigation commands accept two input forms:

- **Name-based (recommended):** `SYMBOL --in FILE` — the broker resolves the symbol name to a position automatically.
- **Positional:** `--in FILE:LINE:COL` — bypass name resolution with an exact position.

Line and column numbers are always **1-based** (matching compiler output and grep).

#### def — go to definition

```sh
# Name-based (preferred):
gopls lspcli def Parse --in ./parser.go

# With line hint for disambiguation:
gopls lspcli def Parse --in ./parser.go:42

# Positional:
gopls lspcli def --in ./parser.go:42:10

# JSON output:
gopls lspcli --json def Parse --in ./parser.go
```

**Output (text):**
```
/abs/path/to/parser.go:15:6
```

**Output (JSON):**
```json
[{"uri":"file:///abs/path/to/parser.go","range":{"start":{"line":14,"character":5},"end":{"line":14,"character":10}}}]
```

#### refs — find references

```sh
gopls lspcli refs Parse --in ./parser.go
gopls lspcli --json refs Parse --in ./parser.go
```

Returns all locations where the symbol is referenced.

#### hover — type and documentation

```sh
gopls lspcli hover Parse --in ./parser.go
gopls lspcli --json hover Parse --in ./parser.go
```

Returns the type signature and documentation for the symbol.

#### impl — find implementations

```sh
gopls lspcli impl Handler --in ./handler.go
```

Returns locations of all types that implement the interface.

#### prep-calls — call hierarchy

```sh
gopls lspcli prep-calls Serve --in ./server.go
```

Returns the call hierarchy item for the symbol, which can be used to explore incoming and outgoing calls.

#### symbols — list symbols in a file

```sh
gopls lspcli symbols ./parser.go
gopls lspcli --json symbols ./parser.go
```

Lists all symbols (functions, types, variables, constants) defined in the file. Useful for exploring a file's API surface.

#### wsymbols — search workspace symbols

```sh
gopls lspcli wsymbols "Parse"
gopls lspcli --json wsymbols "Parse"
```

Searches the workspace for symbols matching the query string.

### Editing

#### rename — rename a symbol across files

```sh
# Rename and apply changes:
gopls lspcli rename Parse --in ./parser.go --to ParseAST

# Preview changes without applying (dry run):
gopls lspcli rename Parse --in ./parser.go --to ParseAST --dry-run

# JSON output (includes raw WorkspaceEdit for programmatic use):
gopls lspcli --json rename Parse --in ./parser.go --to ParseAST --dry-run
```

**Output (text):**
```
renamed Parse -> ParseAST
  /abs/path/to/parser.go (3 edits)
  /abs/path/to/parser_test.go (5 edits)
```

Rename is the only write operation. With `--dry-run`, it returns the edit plan without modifying files — always preview first when unsure.

### Diagnostics and sync

#### diagnostics — show errors and warnings

```sh
# Diagnostics for one file:
gopls lspcli diagnostics ./parser.go

# Diagnostics for entire project:
gopls lspcli diagnostics --project

# JSON output:
gopls lspcli --json diagnostics ./parser.go
```

Returns compiler errors and linter warnings. The daemon buffers diagnostics pushed by LSP servers — this command pulls the latest.

#### sync — force file re-sync

```sh
gopls lspcli sync ./parser.go
```

After editing a file outside of the LSP server's awareness, run `sync` to tell the broker to re-read the file from disk and notify the language server. This ensures subsequent queries return results based on the latest file content.

**When to sync:** After writing to a file, run `sync` before querying it. The broker tracks file modifications by mtime and size, so if the file hasn't changed on disk since the last sync, this is a no-op.

### Daemon management

```sh
gopls lspcli daemon status     # show daemon PID, uptime, active sessions
gopls lspcli daemon stop       # graceful shutdown
gopls lspcli daemon restart    # stop + start
gopls lspcli daemon start      # explicit start (usually unnecessary — auto-spawns)
```

### Trust management

Non-Go languages require a `.lsp.json` config file that specifies the LSP server command. Because this config can execute arbitrary binaries, the broker enforces a trust model.

```sh
gopls lspcli trust add /path/to/project    # trust a project root
gopls lspcli trust list                    # show trusted roots
gopls lspcli trust remove /path/to/project # revoke trust
```

**Go projects do not need trust configuration.** Go is auto-detected via `go.mod` and uses the built-in gopls server, so untrusted Go repos are safe to query.

## Global flags

These flags go **before** the subcommand name:

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Output results as JSON (machine-readable) |
| `--no-spawn` | false | Fail if daemon is not already running |
| `--timeout=DUR` | 30s | Wall-clock timeout for the invocation |
| `-v` | false | Verbose: print raw broker responses |

Example: `gopls lspcli --json --timeout=60s def Parse --in ./parser.go`

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Success but empty result (e.g., no definition found, rename produced no changes) |
| 2 | User error (bad arguments, missing required flags) |
| 3 | LSP error (server returned an error, timeout, server crashed) |
| 4 | Broker error (daemon unreachable, protocol version mismatch) |

## Multi-language setup

For languages other than Go, create a `.lsp.json` file in your project root:

```json
{
  "version": 1,
  "servers": {
    "typescript": {
      "command": ["typescript-language-server", "--stdio"],
      "extensionToLanguage": {
        ".ts": "typescript",
        ".tsx": "typescriptreact",
        ".js": "javascript",
        ".jsx": "javascriptreact"
      }
    },
    "python": {
      "command": ["pyright-langserver", "--stdio"],
      "extensionToLanguage": {
        ".py": "python"
      }
    },
    "rust": {
      "command": ["rust-analyzer"],
      "extensionToLanguage": {
        ".rs": "rust"
      }
    }
  }
}
```

Then trust the project: `gopls lspcli trust add /path/to/project`

After that, the same commands work for any language:

```sh
gopls lspcli def Component --in ./App.tsx
gopls lspcli refs parse_args --in ./cli.py
gopls lspcli hover Config --in ./src/config.rs
```

### .lsp.json fields

| Field | Required | Description |
|---|---|---|
| `version` | yes | Must be `1` |
| `servers.<id>.command` | yes | Command and args to start the LSP server (must support stdio) |
| `servers.<id>.extensionToLanguage` | yes | Map file extensions to LSP language IDs |
| `servers.<id>.env` | no | Extra environment variables for the server process |
| `servers.<id>.initializationOptions` | no | Passed to the server's `initialize` request |
| `servers.<id>.settings` | no | Passed as `workspace/didChangeConfiguration` |
| `servers.<id>.startupTimeout` | no | Max time to wait for server init (default: 30s) |
| `servers.<id>.maxRestarts` | no | Max automatic restarts on crash (default: 3) |

## Security and trust model

The broker's trust model exists because `.lsp.json` can specify arbitrary commands to execute. The rules are:

1. **Go projects are always safe.** Go is auto-detected via `go.mod` presence. The broker uses the built-in gopls server — no external command execution. You can safely query any Go repo without trusting it.

2. **Non-Go projects require explicit trust.** Before the broker will read a `.lsp.json` and execute the server commands it specifies, you must run `gopls lspcli trust add /path/to/project`. Trust is stored per-user in `$XDG_CONFIG_HOME/lsp-broker/trusted.json`.

3. **Untrusted non-Go projects get `ErrUntrustedRoot`.** If you query a file in an untrusted project that has a `.lsp.json`, the broker returns an error rather than silently executing the config's commands.

**For untrusted repos:** If you only need Go code intelligence, you're fine — Go auto-detection bypasses the trust gate entirely. If you need non-Go LSP features, inspect the `.lsp.json` first (check what commands it runs), then trust the project explicitly.

## Build and install

### Prerequisites

- Go 1.23 or later
- For Go projects: no additional setup needed
- For other languages: the relevant LSP server binary must be on `$PATH`

### Building

```sh
# From the golang.org/x/tools checkout:
go install ./gopls
```

This produces a single `gopls` binary that includes both `lspcli` and `lspbrokerd` as subcommands.

### PATH setup

Ensure the Go binary directory is on your PATH:

```sh
export PATH="$GOPATH/bin:$PATH"
# or if using the default GOPATH:
export PATH="$HOME/go/bin:$PATH"
```

Verify: `gopls lspcli --help` should print the usage text.

## Workflow patterns for agents

### Pattern 1: Explore an unfamiliar codebase

```sh
# List all symbols in the entry point:
gopls lspcli symbols ./cmd/main.go

# Find the definition of the main type:
gopls lspcli def Server --in ./cmd/main.go

# Explore its methods:
gopls lspcli symbols ./internal/server.go

# Get type info on a method:
gopls lspcli hover ServeHTTP --in ./internal/server.go
```

### Pattern 2: Understand a function before modifying it

```sh
# Who calls this function?
gopls lspcli refs ProcessRequest --in ./handler.go

# What does it do? (type + docs)
gopls lspcli hover ProcessRequest --in ./handler.go

# Where is it defined?
gopls lspcli def ProcessRequest --in ./handler.go
```

### Pattern 3: Safe rename

```sh
# Preview the rename:
gopls lspcli rename ProcessRequest --in ./handler.go --to HandleRequest --dry-run

# If the preview looks correct, apply:
gopls lspcli rename ProcessRequest --in ./handler.go --to HandleRequest

# Verify no diagnostics:
gopls lspcli diagnostics --project
```

### Pattern 4: Edit-sync-check cycle

```sh
# After editing a file:
gopls lspcli sync ./handler.go

# Check for errors:
gopls lspcli diagnostics ./handler.go

# Verify the change didn't break references:
gopls lspcli refs HandleRequest --in ./handler.go
```

## Troubleshooting

### "no language server configured for this file type"
The file extension doesn't match any configured LSP server. For Go files, ensure `go.mod` exists in a parent directory. For other languages, create a `.lsp.json` with the appropriate `extensionToLanguage` mapping.

### "project root not trusted"
The project has a `.lsp.json` but hasn't been trusted. Run `gopls lspcli trust add /path/to/project`. Go-only projects don't need this.

### "broker not running (--no-spawn)"
You passed `--no-spawn` but the daemon isn't running. Either remove the flag (the daemon auto-spawns) or start it manually: `gopls lspcli daemon start`.

### "broker protocol version mismatch"
The running daemon was built from a different gopls version than the CLI. Run `gopls lspcli daemon restart` to pick up the new version.

### Stale results after editing
Run `gopls lspcli sync FILE` after editing to refresh the LSP server's view of the file.

### Slow first invocation
The first `lspcli` call spawns the daemon and initializes the LSP server, which may take several seconds (especially for large Go modules). Subsequent calls reuse the warm session and complete in under 200ms.

### Symbol not found / ambiguous symbol
Name-based lookup searches the specified file. If the symbol exists in a different file, use `--in` to point at the correct file. If multiple symbols match (e.g., overloaded names), add a line hint: `def Parse --in ./parser.go:42`.

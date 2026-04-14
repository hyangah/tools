---
name: goplscli
description: Use gopls cli for Go code intelligence — go-to-definition, find-references, hover, rename, diagnostics, symbols. Use when navigating Go code, understanding types/APIs, renaming symbols safely, or checking for compile errors after edits.
allowed-tools: Bash(gopls cli *)
---

# gopls cli — Code Intelligence for Go

Use `gopls cli` instead of grep when you need type-aware code navigation. It gives you go-to-definition, find-references, hover info, rename, diagnostics, and symbol search — all through shell commands.

Connects to the gopls daemon (shared with your editor). No separate daemon to manage.

## Commands

**Navigate code:**
```sh
gopls cli def FILE:LINE:COL              # go to definition
gopls cli refs FILE:LINE:COL             # find all references
gopls cli hover FILE:LINE:COL            # type signature + docs
gopls cli impl FILE:LINE:COL             # find interface implementations
gopls cli symbols FILE                   # list all symbols in a file
gopls cli wsymbols "QUERY"               # search workspace for symbols
```

**Edit safely:**
```sh
gopls cli rename FILE:LINE:COL --to NEWNAME   # rename symbol
```

**Check errors after edits:**
```sh
gopls cli sync FILE                      # re-sync file after editing
gopls cli diagnostics FILE               # errors/warnings for file
```

## Position format

All positions use `FILE:LINE:COL` where:
- **LINE** is 1-based (matching compiler output)
- **COL** is 1-based UTF-8 byte column (matching `go/token.Position.Column`)

These match what `go build`, `go vet`, and `grep -n` produce.

## Flags

- `--json` — machine-readable JSON output
- `--address=ADDR` — daemon unix socket address

Example: `gopls cli --json def ./main.go:42:10`

## Key workflow: edit-sync-check

After editing a file, always sync before querying:
```sh
# 1. Edit the file
# 2. Tell gopls about changes:
gopls cli sync ./modified_file.go
# 3. Check for errors:
gopls cli diagnostics ./modified_file.go
```

## When NOT to use

- Running tests → `go test`
- Building → `go build`
- Formatting → `gofmt`
- Text search → `grep` / `rg`
- Reading file content → `cat` / Read tool
- Go documentation → `go doc`

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Empty result (no definition found, etc.) |
| 2 | Bad arguments |
| 3 | Server error |
| 4 | Connection error |

## Troubleshooting

- **Stale results after editing** → run `gopls cli sync FILE` first
- **Slow first call** → normal (session cold start ~100ms). Subsequent calls are <1ms
- **Connection refused** → ensure gopls daemon is running with `--cli.listen`

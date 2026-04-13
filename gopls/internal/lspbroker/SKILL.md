---
name: lspcli
description: Use gopls lspcli for Go code intelligence — go-to-definition, find-references, hover, rename, diagnostics, symbols. Use when navigating Go code, understanding types/APIs, renaming symbols safely, or checking for compile errors after edits.
allowed-tools: Bash(gopls lspcli *)
---

# gopls lspcli — Code Intelligence for Go

Use `gopls lspcli` instead of grep when you need type-aware code navigation. It gives you go-to-definition, find-references, hover info, rename, diagnostics, and symbol search — all through shell commands.

The daemon auto-spawns on first use. No setup needed for Go projects.

## Commands

**Navigate code:**
```sh
gopls lspcli def SYMBOL --in FILE           # go to definition
gopls lspcli refs SYMBOL --in FILE          # find all references
gopls lspcli hover SYMBOL --in FILE         # type signature + docs
gopls lspcli impl SYMBOL --in FILE          # find interface implementations
gopls lspcli symbols FILE                   # list all symbols in a file
gopls lspcli wsymbols "QUERY"               # search workspace for symbols
```

**Edit safely:**
```sh
gopls lspcli rename SYMBOL --in FILE --to NEWNAME --dry-run  # preview
gopls lspcli rename SYMBOL --in FILE --to NEWNAME            # apply
```

**Check errors after edits:**
```sh
gopls lspcli sync FILE                      # re-sync file with LSP server
gopls lspcli diagnostics FILE               # errors/warnings for one file
gopls lspcli diagnostics --project          # errors/warnings for all files
```

## Input forms

- **Name-based (preferred):** `def SYMBOL --in FILE` — broker resolves the name
- **With line hint:** `def SYMBOL --in FILE:42` — disambiguate same-name symbols
- **Positional:** `def --in FILE:42:10` — exact position, bypass name resolution

Line and column are always **1-based** (matching compiler output).

## Global flags (before subcommand)

- `--json` — machine-readable JSON output
- `--timeout=DUR` — wall-clock timeout (default 30s)

Example: `gopls lspcli --json def SYMBOL --in FILE`

## Key workflow: edit-sync-check

After editing a file, always sync before querying:
```sh
# 1. Edit the file (write tool, etc.)
# 2. Tell the LSP server about changes:
gopls lspcli sync ./modified_file.go
# 3. Check for errors:
gopls lspcli diagnostics ./modified_file.go
# 4. Check downstream breakage:
gopls lspcli diagnostics --project
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
| 3 | LSP server error |
| 4 | Broker error |

## Troubleshooting

- **Stale results after editing** → run `gopls lspcli sync FILE` first
- **"no language server configured"** → ensure `go.mod` exists in a parent directory
- **Slow first call** → normal (daemon + gopls cold start). Subsequent calls are fast (<200ms)
- **"ambiguous symbol"** → add a line hint: `def SYMBOL --in FILE:42`

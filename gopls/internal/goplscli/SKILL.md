---
name: goplscli
description: Use gopls cli for Go code intelligence — go-to-definition, find-references, hover, rename with dry-run preview, diagnostics, symbols. Use when navigating Go code, understanding types/APIs, renaming symbols safely, or quickly checking files for errors after editing (faster than go build).
allowed-tools: Bash(gopls cli *)
---

# gopls cli — Code Intelligence for Go

Use `gopls cli` instead of grep when you need type-aware code navigation. It gives you go-to-definition, find-references, hover info, rename, diagnostics, and symbol search — all through shell commands.

Connects to the gopls daemon (shared with your editor). No separate daemon to manage.

## Commands

**Navigate code (by symbol name — preferred, no position needed):**
```sh
gopls cli def SYMBOL --in FILE           # go to definition by name
gopls cli refs SYMBOL --in FILE          # find all references by name
gopls cli hover SYMBOL --in FILE         # type signature + docs
gopls cli impl SYMBOL --in FILE          # find interface implementations
gopls cli rename SYMBOL --in FILE --to NEWNAME  # preview rename edits
```

SYMBOL is a Go identifier name. Just use the bare name — method receiver
qualification is handled automatically:
- Functions: `FindProjectRoot`, `main`
- Types: `CLIHandler`, `GoSession`
- Methods: `SessionFor` (matches `(*CLIHandler).SessionFor` automatically)
- Fields: `IdleTimeout`
- Constants/variables: `maxMessageSize`, `MethodDefinition`

Full qualified forms also work: `(*GoSession).Definition` or `GoSession.Definition`.

If SYMBOL is ambiguous (multiple matches in the file), add a line hint:
```sh
gopls cli def Close --in handler.go:156  # disambiguate by line number
```

**Navigate code (by position — use when you already have FILE:LINE:COL):**
```sh
gopls cli def FILE:LINE:COL              # go to definition
gopls cli refs FILE:LINE:COL             # find all references
gopls cli hover FILE:LINE:COL            # type signature + docs
gopls cli impl FILE:LINE:COL             # find interface implementations
gopls cli rename FILE:LINE:COL --to NEWNAME  # preview rename edits
```

**Search and list:**
```sh
gopls cli symbols FILE                   # list all symbols in a file
gopls cli wsymbols "QUERY"               # search workspace for symbols
```

`symbols` output uses Go receiver syntax: `(*GoSession).Root`, `NewGoSession`.
`wsymbols` accepts fuzzy queries: `"Session"`, `"GoSession.Definition"`,
`"FindProject"`. Use `Type.Method` format (no pointer) to narrow to a
specific type's method.

**Check errors after edits (faster than go build):**
```sh
gopls cli sync FILE                      # re-sync file after editing
gopls cli diagnostics FILE               # errors/warnings for file
```

## Input forms

- **Name-based (preferred):** `def SYMBOL --in FILE` — resolve the symbol by name
- **With line hint:** `def SYMBOL --in FILE:42` — disambiguate same-name symbols
- **Positional:** `def FILE:42:10` — exact position, when you already have line:col

Agents usually know symbol names but not positions. Start with name-based
queries and fall back to positional only when processing compiler output
(which provides FILE:LINE:COL).

## Position format

All positions use `FILE:LINE:COL` where:
- **LINE** is 1-based (matching compiler output)
- **COL** is 1-based UTF-8 byte column (matching `go/token.Position.Column`)

These match what `go build`, `go vet`, and `grep -n` produce.

## Flags

- `--json` — machine-readable JSON output
- `--address=ADDR` — daemon unix socket address

Example: `gopls cli --json def GoSession --in session.go`

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
- **"ambiguous symbol"** → add a line hint: `def SYMBOL --in FILE:42`
- **Rename misses test files** → rename may not cover external test packages (`pkg_test`). Verify with `refs` or grep after renaming

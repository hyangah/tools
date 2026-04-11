# Go Tools & Gopls: Agent Index

This document serves as a quick index for contributing to `golang.org/x/tools` and `gopls`. Instead of duplicating documentation, please refer to the following canonical sources:

## General Overview
*   **`x/tools` repo:** [README.md](README.md)
*   **`gopls` tool:** [gopls/README.md](gopls/README.md) and [gopls/doc/index.md](gopls/doc/index.md)

## Contributing to `gopls`
Start with the primary contribution guide: [**`gopls/doc/contributing.md`**](gopls/doc/contributing.md).

Key sections in the guide:
*   **Issues & Planning**: Filing issues, claiming work, and planning implementations. (Top of [contributing.md](gopls/doc/contributing.md))
*   **Building**: How to compile and verify your local build. ([contributing.md#build](gopls/doc/contributing.md#build))
*   **Error Handling**: How to use the `bug` package to prevent crashing user editors. ([contributing.md#error-handling](gopls/doc/contributing.md#error-handling))
*   **Testing**: Understanding the `marker` and `integration` test frameworks, and CI. ([contributing.md#testing](gopls/doc/contributing.md#testing))
*   **Debugging**: Using the debug server and OpenTelemetry. ([contributing.md#debugging](gopls/doc/contributing.md#debugging))

## Architecture & Troubleshooting
*   **Implementation Overview**: [gopls/doc/design/implementation.md](gopls/doc/design/implementation.md)
*   **Troubleshooting Guide**: [gopls/doc/troubleshooting.md](gopls/doc/troubleshooting.md)

## Code Navigation

Claude Code provides a built-in `LSP` tool backed by gopls. Use it for precision navigation instead of grep+Read when you already know a file location:

- `hover` — get the type/signature/doc of any identifier. Replaces grep-then-read on large files like `gopls/internal/protocol/tsprotocol.go`.
- `goToDefinition` — jump to where a symbol is declared.
- `documentSymbol` — list all exported names in a file without reading it.
- `findReferences` — all call sites of a function across the workspace.

**line and character are 1-based.** If `workspaceSymbol` returns nothing, gopls is still indexing — fall back to `Grep` and retry after a build.

### gopls cli (agent-facing CLI)

`gopls cli` provides the same code intelligence as shell commands, sharing
the gopls daemon with the editor for zero extra memory cost. See
[gopls/internal/goplscli/SKILL.md](gopls/internal/goplscli/SKILL.md) for
the full command reference.

Quick reference:
```sh
gopls cli def FILE:LINE:COL       # go to definition
gopls cli refs FILE:LINE:COL      # find references
gopls cli hover FILE:LINE:COL     # type + docs
gopls cli symbols FILE            # list symbols
gopls cli diagnostics FILE        # errors/warnings
gopls cli sync FILE               # re-sync after edit
```

Positions are 1-based with UTF-8 byte columns (matching Go compiler output).

## Additional References
*   **Go Contribution Guidelines**: [https://go.dev/doc/contribute](https://go.dev/doc/contribute)

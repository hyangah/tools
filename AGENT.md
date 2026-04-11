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

## Additional References
*   **Go Contribution Guidelines**: [https://go.dev/doc/contribute](https://go.dev/doc/contribute)

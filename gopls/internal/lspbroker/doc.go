// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lspbroker implements a language-agnostic broker for
// shell-based AI coding agents.
//
// The broker is a long-lived per-user daemon that manages LSP server
// subprocesses on behalf of one or more agents, exposed through a thin
// command-line interface. Agents invoke the CLI via their shell-exec
// tool and receive pre-formatted results — they need not speak JSON-RPC,
// LSP, or MCP.
//
// # Packaging
//
// The broker ships as two subcommands of the gopls binary:
//
//   - gopls lspcli <subcommand> ... — thin, agent-facing CLI
//     (one process per invocation).
//   - gopls lspbrokerd [--detach] — the long-lived daemon
//     (normally spawned internally by the CLI, rarely invoked
//     directly).
//
// Both subcommands are registered into gopls's existing
// golang.org/x/tools/internal/tool command framework. There are no new
// top-level binaries. See the project's ADR-003 for the rationale.
//
// # Subpackages
//
// The package tree is:
//
//   - lspbroker               — broker core (daemon, sessions, sockets)
//   - lspbroker/cmd           — subcommand entrypoints wired into gopls
//   - lspbroker/lspclient     — generic JSON-RPC LSP client library
//   - lspbroker/goadapter     — Go-featured path (gopls client,
//     extended commands)
//   - lspbroker/format        — shared output formatters
//
// The package does not modify gopls/internal/lsprpc,
// gopls/internal/mcp, or gopls/internal/protocol — it only consumes
// them.
package lspbroker

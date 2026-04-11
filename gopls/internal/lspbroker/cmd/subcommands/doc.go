// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package subcommands contains the per-subcommand implementations for
// the gopls lspcli command.
//
// Each file in this package corresponds to one or more CLI subcommands:
//
//   - def.go        — `gopls lspcli def FILE LINE COL`
//   - resolve.go    — shared path resolution helpers
//   - spawn.go      — daemon auto-spawn and socket connection logic
//   - client.go     — broker JSON-RPC client over unix socket
//
// Subcommand functions return an exit code and an error. Callers
// (cmd/lspcli.go) convert these to the exit codes defined in
// designs/06-cli-surface.md: 0 success, 1 empty, 2 user error,
// 3 LSP error, 4 broker error.
package subcommands

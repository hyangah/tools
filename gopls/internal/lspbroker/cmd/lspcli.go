// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/tools/internal/tool"
)

// RunLSPCLI is the entry point for `gopls lspcli <subcommand> ...`.
// It parses the first positional argument as a subcommand name and
// dispatches to the implementation for that subcommand.
//
// Phase 0: no subcommands are registered yet, so any invocation with
// arguments returns a tool.CommandLineErrorf that causes the tool
// framework to print help. `gopls lspcli --help` is handled by the
// flag package before RunLSPCLI is called.
func RunLSPCLI(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return tool.CommandLineErrorf("lspcli: must provide subcommand")
	}
	return tool.CommandLineErrorf("lspcli: unknown subcommand %q (no subcommands are implemented yet in Phase 0)", args[0])
}

// PrintLSPCLIHelp writes the lspcli long-form help to w. It is
// invoked by the tool.Application wrapper's DetailedHelp method in
// gopls/internal/cmd/lspbroker.go.
func PrintLSPCLIHelp(w io.Writer) {
	fmt.Fprint(w, `
lspcli is the agent-facing CLI for the LSP broker. Agents invoke
subcommands like "gopls lspcli def FILE LINE COL" via their shell
tool and receive pre-formatted results.

The CLI is a thin, stateless wrapper around the broker daemon: on
first invocation it auto-spawns "gopls lspbrokerd --detach" and
communicates with it over a unix domain socket.

Phase 0: no subcommands are registered yet. "gopls lspcli --help"
exists so that agents, tests, and CI can verify the subcommand is
wired into the gopls binary. Real subcommands (def, refs, hover,
symbols, daemon, ...) land in Phase 1 and later as the relevant
workstreams complete.
`)
}

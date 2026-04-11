// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

// This file registers the `gopls lspcli` and `gopls lspbrokerd`
// subcommands with gopls's tool framework. Both subcommands
// delegate their Run logic into
// golang.org/x/tools/gopls/internal/lspbroker/cmd so that the broker
// code lives entirely under gopls/internal/lspbroker/ and this file
// stays a thin wrapper — see ADR-003 for the single-binary rationale
// and IMPLEMENTATION_PLAN.md for the package layout.

import (
	"context"
	"flag"

	brokercmd "golang.org/x/tools/gopls/internal/lspbroker/cmd"
)

// lspcli implements the `gopls lspcli` subcommand — the
// agent-facing CLI for the LSP broker.
type lspcli struct {
	app *Application
}

func (c *lspcli) Name() string      { return "lspcli" }
func (c *lspcli) Parent() string    { return c.app.Name() }
func (c *lspcli) Usage() string     { return "<subcommand> [arg]..." }
func (c *lspcli) ShortHelp() string { return "LSP broker CLI (agent-facing)" }

func (c *lspcli) DetailedHelp(f *flag.FlagSet) {
	brokercmd.PrintLSPCLIHelp(f.Output())
	printFlagDefaults(f)
}

func (c *lspcli) Run(ctx context.Context, args ...string) error {
	return brokercmd.RunLSPCLI(ctx, args...)
}

// lspbrokerd implements the `gopls lspbrokerd` subcommand — the
// long-lived broker daemon process.
type lspbrokerd struct {
	app *Application
}

func (c *lspbrokerd) Name() string      { return "lspbrokerd" }
func (c *lspbrokerd) Parent() string    { return c.app.Name() }
func (c *lspbrokerd) Usage() string     { return "[flags]" }
func (c *lspbrokerd) ShortHelp() string { return "LSP broker daemon (long-lived)" }

func (c *lspbrokerd) DetailedHelp(f *flag.FlagSet) {
	brokercmd.PrintLSPBrokerdHelp(f.Output())
	printFlagDefaults(f)
}

func (c *lspbrokerd) Run(ctx context.Context, args ...string) error {
	return brokercmd.RunLSPBrokerd(ctx, args...)
}

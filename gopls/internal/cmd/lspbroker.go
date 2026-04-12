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
	"time"

	brokercmd "golang.org/x/tools/gopls/internal/lspbroker/cmd"
)

// lspcli implements the `gopls lspcli` subcommand — the
// agent-facing CLI for the LSP broker.
type lspcli struct {
	app *Application

	JSON    bool          `flag:"json" help:"output results as JSON"`
	NoSpawn bool          `flag:"no-spawn" help:"fail if broker daemon is not already running"`
	Timeout time.Duration `flag:"timeout" help:"wall-clock timeout (default 30s)"`
	Verbose bool          `flag:"v" help:"verbose: print raw broker responses"`
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
	gf := brokercmd.GlobalFlags{
		JSON:    c.JSON,
		NoSpawn: c.NoSpawn,
		Timeout: c.Timeout,
		Verbose: c.Verbose,
	}
	return brokercmd.RunLSPCLI(ctx, gf, args...)
}

// lspbrokerd implements the `gopls lspbrokerd` subcommand — the
// long-lived broker daemon process.
//
// Flags are declared as exported struct fields so that gopls's tool
// framework registers them in its FlagSet before calling Run.
type lspbrokerd struct {
	app *Application

	// Detach causes the daemon to spawn a background process and exit.
	// Used by `gopls lspcli` when auto-spawning the daemon.
	Detach bool `flag:"detach" help:"run daemon in background and exit"`

	// CacheDir overrides the default cache directory. Defaults to
	// $XDG_CACHE_HOME/lsp-broker/<buildid>/ when unset.
	CacheDir string `flag:"cache-dir" help:"override cache directory (default: auto from build-id)"`
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
	return brokercmd.RunLSPBrokerd(ctx, c.Detach, c.CacheDir)
}

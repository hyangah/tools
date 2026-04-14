// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

// This file registers the `gopls cli` subcommand.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"golang.org/x/tools/gopls/internal/goplscli"
	clicmd "golang.org/x/tools/gopls/internal/goplscli/cmd"
)

// goplsCLI implements the `gopls cli` subcommand — the
// agent-facing CLI for Go code intelligence.
type goplsCLI struct {
	app *Application

	JSON    bool   `flag:"json" help:"output results as JSON"`
	Address string `flag:"address" help:"daemon unix socket address (default: auto-detect)"`
}

func (c *goplsCLI) Name() string      { return "cli" }
func (c *goplsCLI) Parent() string    { return c.app.Name() }
func (c *goplsCLI) Usage() string     { return "<command> [args]" }
func (c *goplsCLI) ShortHelp() string { return "Go code intelligence CLI (for AI agents)" }

func (c *goplsCLI) DetailedHelp(f *flag.FlagSet) {
	fmt.Fprint(f.Output(), `
gopls cli provides Go code intelligence for AI coding agents.
It communicates with a gopls daemon over a unix socket.

Commands:
  def FILE:LINE:COL           go to definition
  refs FILE:LINE:COL          find references
  hover FILE:LINE:COL         show type and documentation
  impl FILE:LINE:COL          find implementations
  symbols FILE                list symbols in file
  wsymbols QUERY              search workspace symbols
  rename FILE:LINE:COL --to NAME  rename symbol
  sync FILE                   re-sync file after edit
  diagnostics FILE            show errors/warnings

Positions are 1-based line:column (UTF-8 byte column).

cli-flags:
`)
	printFlagDefaults(f)
}

func (c *goplsCLI) Run(ctx context.Context, args ...string) error {
	address := c.Address
	if address == "" {
		var err error
		address, err = goplscli.AutoConnect(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(4)
		}
	}

	exitCode := clicmd.Run(ctx, address, c.JSON, args, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"

	cli "golang.org/x/tools/gopls/internal/goplscli/cmd"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/tool"
)

// cliCmd implements the `gopls cli` subcommand for AI agent use.
type cliCmd struct {
	app  *Application
	JSON bool `flag:"json" help:"output results as JSON"`
}

func (c *cliCmd) Name() string      { return "cli" }
func (c *cliCmd) Parent() string    { return c.app.Name() }
func (c *cliCmd) Usage() string     { return "[cli-flags] <command> [args]" }
func (c *cliCmd) ShortHelp() string { return "agent-friendly code intelligence CLI" }
func (c *cliCmd) DetailedHelp(f *flag.FlagSet) {
	fmt.Fprint(f.Output(), `
Agent-friendly Go code intelligence powered by the gopls daemon.
Supports symbol-based lookup and terse output for AI agents.

Commands:
  def       Find definition (FILE:LINE:COL or SYMBOL --in FILE)
  refs      Find references
  hover     Show type and documentation
  impl      Find implementations
  symbols   List symbols in a file
  wsymbols  Search workspace symbols
  rename    Rename a symbol (FILE:LINE:COL --to NEWNAME)
  check     Report diagnostics (workspace, or filtered to FILE...)

Examples:
  $ gopls cli def ./main.go:10:5
  $ gopls cli def Parse --in ./parser.go
  $ gopls cli refs NewSession --in ./session.go
  $ gopls cli hover ./cache/session.go:39:6
  $ gopls cli symbols ./session.go
  $ gopls cli wsymbols NewSession
  $ gopls cli rename ./session.go:39:6 --to CreateSession
  $ gopls cli check
  $ gopls cli check ./main.go

cli-flags:
`)
	printFlagDefaults(f)
}

func (c *cliCmd) Run(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return tool.CommandLineErrorf("usage: gopls cli <command> [args]")
	}

	// Opt into the CLI capability profile for this subcommand. Must be
	// set before connect — the profile is consumed in initParams during
	// the Initialize handshake, and in the cliServer wrapping decision
	// below. Diagnostic-consuming subcommands (check, vet, codeaction,
	// fix) get pull-diagnostic capabilities layered on top of the base
	// CLI profile; everything else uses the base profile.
	profile := cliProfileFor(args[0])
	c.app.profile = &profile

	conn, _, err := c.app.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.terminate(ctx)

	// When the profile calls for it, skip the cliServer wrapper that
	// sends DidOpen before every query. The server reads target URIs
	// from disk, and in a pooled daemon the session-scoped file watcher
	// keeps snapshots current across connections. See the 2026-04-18
	// investigation note in gopls/CLAUDE.md.
	var srv protocol.Server = conn.server
	if !profile.skipDidOpen {
		srv = &cliServer{client: conn, Server: conn.server}
	}
	exitCode := cli.Run(ctx, srv, c.JSON, args, os.Stdout)
	if exitCode != 0 {
		return fmt.Errorf("exit code %d", exitCode)
	}
	return nil
}

// cliServer wraps protocol.Server to open files before queries, for
// profiles that do not declare skipDidOpen. Kept as a legacy fallback
// while we validate the skip-DidOpen path; can be removed once the CLI
// profile is the only consumer and its skipDidOpen is known-good in
// production.
type cliServer struct {
	client          *client
	protocol.Server // delegates all methods; overridden selectively below
}

func (s *cliServer) Definition(ctx context.Context, params *protocol.DefinitionParams) ([]protocol.Location, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.Definition(ctx, params)
}

func (s *cliServer) References(ctx context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.References(ctx, params)
}

func (s *cliServer) Hover(ctx context.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.Hover(ctx, params)
}

func (s *cliServer) Implementation(ctx context.Context, params *protocol.ImplementationParams) ([]protocol.Location, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.Implementation(ctx, params)
}

func (s *cliServer) DocumentSymbol(ctx context.Context, params *protocol.DocumentSymbolParams) ([]any, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.DocumentSymbol(ctx, params)
}

func (s *cliServer) Symbol(ctx context.Context, params *protocol.WorkspaceSymbolParams) ([]protocol.SymbolInformation, error) {
	return s.Server.Symbol(ctx, params)
}

func (s *cliServer) Rename(ctx context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	if _, err := s.client.openFile(ctx, params.TextDocument.URI); err != nil {
		return nil, err
	}
	return s.Server.Rename(ctx, params)
}

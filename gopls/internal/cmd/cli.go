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

Examples:
  $ gopls cli def ./main.go:10:5
  $ gopls cli def Parse --in ./parser.go
  $ gopls cli refs NewSession --in ./session.go
  $ gopls cli hover ./cache/session.go:39:6
  $ gopls cli symbols ./session.go
  $ gopls cli wsymbols NewSession
  $ gopls cli rename ./session.go:39:6 --to CreateSession

cli-flags:
`)
	printFlagDefaults(f)
}

func (c *cliCmd) Run(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return tool.CommandLineErrorf("usage: gopls cli <command> [args]")
	}

	// Opt into the CLI capability profile: no push diagnostics, no
	// progress. Must be set before connect — the profile is consumed in
	// initParams during the Initialize handshake.
	c.app.profile = &cliClientProfile

	conn, _, err := c.app.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.terminate(ctx)

	// The CLI wraps the conn.server and conn (for openFile) into a
	// cliServer that handles file opening before queries.
	cs := &cliServer{client: conn, Server: conn.server}
	exitCode := cli.Run(ctx, cs, c.JSON, args, os.Stdout)
	if exitCode != 0 {
		return fmt.Errorf("exit code %d", exitCode)
	}
	return nil
}

// cliServer wraps protocol.Server to open files before queries.
// The in-process gopls server requires didOpen before it can serve
// queries on a file (no file watchers in CLI mode).
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

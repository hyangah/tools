// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/cmd/subcommands"
	"golang.org/x/tools/internal/tool"
)

// globalFlags holds the flags that precede the subcommand name and
// apply to all lspcli invocations.
type globalFlags struct {
	JSON    bool
	NoSpawn bool
	Timeout time.Duration
	Verbose bool
}

// RunLSPCLI is the entry point for `gopls lspcli <subcommand> ...`.
// It parses the first positional argument as a subcommand name and
// dispatches to the implementation for that subcommand.
func RunLSPCLI(ctx context.Context, args ...string) error {
	fs := flag.NewFlagSet("lspcli", flag.ContinueOnError)
	var gf globalFlags
	fs.BoolVar(&gf.JSON, "json", false, "output results as JSON")
	fs.BoolVar(&gf.NoSpawn, "no-spawn", false, "fail if broker daemon is not already running")
	fs.DurationVar(&gf.Timeout, "timeout", 30*time.Second, "wall-clock timeout for the invocation")
	fs.BoolVar(&gf.Verbose, "v", false, "verbose: print raw broker responses")

	if err := fs.Parse(args); err != nil {
		return tool.CommandLineErrorf("lspcli: %v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return tool.CommandLineErrorf("lspcli: must provide subcommand")
	}
	sub, subArgs := rest[0], rest[1:]

	// Resolve runtime values needed by subcommands.
	cacheDir := lspbroker.CacheRoot()
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("lspcli: resolve executable: %w", err)
	}

	switch sub {
	case "def":
		flags := subcommands.DefFlags{
			JSON:    gf.JSON,
			NoSpawn: gf.NoSpawn,
			Timeout: gf.Timeout,
		}
		exitCode, runErr := subcommands.RunDef(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "refs":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunRefs(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "hover":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunHover(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "impl":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunImpl(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "prep-calls":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunPrepCalls(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "symbols":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunSymbols(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "wsymbols":
		flags := subcommands.DefFlags{JSON: gf.JSON, NoSpawn: gf.NoSpawn, Timeout: gf.Timeout}
		exitCode, runErr := subcommands.RunWsymbols(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "daemon":
		exitCode, runErr := subcommands.RunDaemon(ctx, subArgs, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "trust":
		if err := subcommands.RunTrust(ctx, subArgs); err != nil {
			return err
		}
		return nil

	case "diagnostics":
		flags := subcommands.DiagnosticsFlags{
			JSON:    gf.JSON,
			NoSpawn: gf.NoSpawn,
			Timeout: gf.Timeout,
		}
		exitCode, runErr := subcommands.RunDiagnostics(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "sync":
		flags := subcommands.SyncFlags{
			NoSpawn: gf.NoSpawn,
			Timeout: gf.Timeout,
		}
		exitCode, runErr := subcommands.RunSync(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	case "rename":
		flags := subcommands.RenameFlags{
			JSON:    gf.JSON,
			NoSpawn: gf.NoSpawn,
			Timeout: gf.Timeout,
		}
		exitCode, runErr := subcommands.RunRename(ctx, subArgs, flags, cacheDir, selfPath, os.Stdout)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		if exitCode != 0 {
			return &exitError{code: exitCode}
		}
		return nil

	default:
		return tool.CommandLineErrorf("lspcli: unknown subcommand %q", sub)
	}
}

// PrintLSPCLIHelp writes the lspcli long-form help to w. It is
// invoked by the tool.Application wrapper's DetailedHelp method in
// gopls/internal/cmd/lspbroker.go.
func PrintLSPCLIHelp(w io.Writer) {
	fmt.Fprint(w, `lspcli is the agent-facing CLI for the LSP broker. It communicates with
a background daemon (lspbrokerd) that manages LSP server sessions.

The CLI auto-spawns "gopls lspbrokerd --detach" on first use. The
daemon manages connections to language servers (gopls for Go, or any
LSP server configured in .lsp.json for other languages).

Global flags (before subcommand):
  --json        output results as JSON
  --no-spawn    fail if the broker daemon is not already running
  --timeout=DUR wall-clock timeout (default 30s)
  -v            verbose: print raw broker responses

Subcommands:

  Code navigation:
    def SYMBOL --in FILE       go to definition
    refs SYMBOL --in FILE      find all references
    hover SYMBOL --in FILE     show type/documentation
    impl SYMBOL --in FILE      find implementations
    prep-calls SYMBOL --in FILE  call hierarchy (prepare)
    symbols FILE               list symbols in a file
    wsymbols QUERY             search workspace symbols

  Editing:
    rename SYMBOL --in FILE --to NEWNAME  rename across files (--dry-run to preview)

  Diagnostics & sync:
    diagnostics FILE           show compiler/linter diagnostics
    sync FILE                  force file re-sync with the LSP server

  Daemon management:
    daemon start               start the daemon (usually auto-spawned)
    daemon status              show daemon status
    daemon stop                stop the daemon
    daemon restart             restart the daemon

  Trust:
    trust add DIR              add a project root to the trust list
    trust list                 list trusted roots
    trust remove DIR           remove a trusted root

All positional subcommands accept either name-based (SYMBOL --in FILE)
or positional (FILE LINE COL) input. Use --json for machine-readable output.
`)

}

// exitError is returned from RunLSPCLI when the CLI should exit with a
// specific non-zero code. The tool framework converts non-nil errors to
// exit code 1; our top-level runner in gopls/internal/cmd/lspbroker.go
// inspects this type to use the embedded code instead.
type exitError struct {
	code int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// ExitCode returns the exit code carried by the error, or 0 if e is nil.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if asExitError(err, &ee) {
		return ee.code
	}
	return 1
}

// asExitError is a type assertion helper to avoid importing errors.As
// in packages that only need this one check.
func asExitError(err error, target **exitError) bool {
	e, ok := err.(*exitError)
	if ok {
		*target = e
	}
	return ok
}

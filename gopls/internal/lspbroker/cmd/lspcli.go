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

	default:
		return tool.CommandLineErrorf("lspcli: unknown subcommand %q", sub)
	}
}

// PrintLSPCLIHelp writes the lspcli long-form help to w. It is
// invoked by the tool.Application wrapper's DetailedHelp method in
// gopls/internal/cmd/lspbroker.go.
func PrintLSPCLIHelp(w io.Writer) {
	fmt.Fprint(w, `
lspcli is the agent-facing CLI for the LSP broker. Agents invoke
subcommands like "gopls lspcli def FILE LINE COL" via their shell
tool and receive pre-formatted results.

The CLI auto-spawns "gopls lspbrokerd --detach" on first use and
communicates with it over a unix domain socket.

Global flags (before subcommand):
  --json        output results as JSON
  --no-spawn    fail if the broker daemon is not already running
  --timeout=DUR wall-clock timeout (default 30s)
  -v            verbose: print raw broker responses

Subcommands (Phase 1):
  def FILE LINE COL   go to definition

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

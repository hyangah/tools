// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/format"
)

// DefFlags holds the parsed flags and positional arguments for the
// "def" subcommand.
type DefFlags struct {
	// JSON requests --json output instead of formatted text.
	JSON bool

	// NoSpawn prevents daemon auto-spawn; fails with exit code 4 if the
	// broker is not already running.
	NoSpawn bool

	// Timeout is the wall-clock limit for the whole invocation.
	Timeout time.Duration
}

// RunDef implements `gopls lspcli def FILE LINE COL`.
//
// args must be exactly [FILE, LINE, COL]. cacheDir is the broker cache
// directory. selfPath is the absolute path of the running gopls binary.
// w is the output writer (normally os.Stdout).
//
// It returns (exitCode, error):
//
//	0  success, non-empty result
//	1  success, empty result (no definition found)
//	2  user error (bad args, file not found)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunDef(ctx context.Context, args []string, flags DefFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	if len(args) != 3 {
		return 2, fmt.Errorf("usage: def FILE LINE COL")
	}

	absFile, err := resolveFile(args[0])
	if err != nil {
		return 2, err
	}
	line, err := parseInt(args[1], "LINE")
	if err != nil {
		return 2, err
	}
	char, err := parseInt(args[2], "COL")
	if err != nil {
		return 2, err
	}

	// Apply timeout to the context.
	timeout := flags.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Ensure the broker daemon is running (auto-spawn unless --no-spawn).
	if !flags.NoSpawn {
		if err := EnsureDaemonRunning(ctx, cacheDir, selfPath); err != nil {
			return 4, fmt.Errorf("start broker daemon: %w", err)
		}
	}

	// Connect to the broker.
	bc, err := DialBroker(ctx, cacheDir, selfPath)
	if err != nil {
		if flags.NoSpawn {
			return 4, fmt.Errorf("broker not running (--no-spawn): %w", err)
		}
		return 4, fmt.Errorf("connect to broker: %w", err)
	}
	defer bc.Close()

	// Issue the definition request.
	locs, err := bc.Definition(ctx, absFile, line, char)
	if err != nil {
		// Classify the error.
		code := classifyError(err)
		return code, err
	}

	if len(locs) == 0 {
		return 1, nil // empty result
	}

	// Format output.
	if flags.JSON {
		if err := format.DefinitionJSON(w, locs); err != nil {
			return 4, err
		}
	} else {
		format.Definition(w, absFile, line, char, locs)
	}
	return 0, nil
}

// classifyError maps a broker/LSP error to an exit code.
//
//	3 — LSP error (timeout, server error, content modified)
//	4 — broker error (protocol mismatch, daemon unreachable)
func classifyError(err error) int {
	if err == nil {
		return 0
	}
	// Broker-specific errors get exit code 4.
	if isLSPBrokerError(err) {
		return 4
	}
	// Everything else is treated as an LSP error.
	return 3
}

// isLSPBrokerError returns true for errors that originate from the
// broker layer rather than the LSP server layer.
func isLSPBrokerError(err error) bool {
	for _, target := range []error{
		lspbroker.ErrVersionMismatch,
		lspbroker.ErrUntrustedRoot,
		lspbroker.ErrProjectNotFound,
	} {
		if target != nil && err.Error() == target.Error() {
			return true
		}
	}
	return false
}

// printError writes an error to stderr in the format expected by the CLI.
func printError(msg, hint string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if hint != "" {
		fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
	}
}

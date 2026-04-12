// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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

// RunDef implements `gopls lspcli def [<symbol>] --in <file>[:line[:col]]`.
//
// The CLI shape follows ADR-007/008:
//
//   - def <symbol> --in <file>            name-based, file-scoped
//   - def <symbol> --in <file>:<line>     name-based, line-narrowed
//   - def          --in <file>:<line>:<col>   positional bypass
//
// --in is always required. Missing → exit 2. Symbol + file:line:col → exit 2.
//
// It returns (exitCode, error):
//
//	0  success, non-empty result
//	1  success, empty result (no definition found)
//	2  user error (bad args, file not found, missing --in)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunDef(ctx context.Context, args []string, flags DefFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	// Parse --in flag from args.
	symbol, inValue, err := parseDefArgs(args)
	if err != nil {
		return 2, err
	}

	// Parse --in value: file, file:line, or file:line:col.
	file, line, col, err := parseInValue(inValue)
	if err != nil {
		return 2, err
	}

	// Validate mutual exclusion: symbol + full file:line:col is an error.
	if symbol != "" && col > 0 {
		return 2, fmt.Errorf("cannot specify both <symbol> and file:line:col; use one form:\n  def <symbol> --in <file>[:line]\n  def          --in <file>:<line>:<col>")
	}

	// Validate: positional bypass requires line and col.
	if symbol == "" && col == 0 {
		return 2, fmt.Errorf("positional form requires file:line:col, got %q\n  usage: def --in <file>:<line>:<col>", inValue)
	}

	// Resolve file to absolute path.
	absFile, err := resolveFile(file)
	if err != nil {
		return 2, err
	}

	// Build discriminated params.
	params := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    absFile,
	}
	if symbol != "" {
		// Form A: name-based.
		params.Symbol = symbol
		if line > 0 {
			params.Line = line
		}
	} else {
		// Form B: positional bypass.
		params.Line = line
		params.Character = lspbroker.IntPtr(col)
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
	locs, err := bc.Definition(ctx, params)
	if err != nil {
		code := classifyError(err)
		return code, err
	}

	if len(locs) == 0 {
		return 1, nil // empty result
	}

	// For formatting, use the resolved position (or input position for
	// name-based queries where the exact position isn't known to the CLI).
	inputLine := line
	inputChar := 0
	if col > 0 {
		inputChar = col
	}

	// Format output.
	if flags.JSON {
		if err := format.DefinitionJSON(w, locs); err != nil {
			return 4, err
		}
	} else {
		format.Definition(w, absFile, inputLine, inputChar, locs)
	}
	return 0, nil
}

// parseDefArgs extracts (symbol, inValue) from the CLI args.
// --in is required; returns an error if missing.
func parseDefArgs(args []string) (symbol, inValue string, err error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--in" {
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--in requires a value")
			}
			inValue = args[i+1]
			i++ // skip the value
		} else if strings.HasPrefix(args[i], "--in=") {
			inValue = strings.TrimPrefix(args[i], "--in=")
		} else if strings.HasPrefix(args[i], "-") {
			return "", "", fmt.Errorf("unknown flag %q", args[i])
		} else {
			positional = append(positional, args[i])
		}
	}

	if inValue == "" {
		return "", "", fmt.Errorf("error: --in <file> is required\n  usage: def <symbol> --in <file>[:line[:col]]")
	}

	switch len(positional) {
	case 0:
		// No symbol — positional bypass form.
	case 1:
		symbol = positional[0]
	default:
		return "", "", fmt.Errorf("too many positional arguments; expected at most 1 (symbol), got %d", len(positional))
	}

	return symbol, inValue, nil
}

// parseInValue parses a value like "file", "file:line", or "file:line:col".
// Returns (file, line, col) where line and col are 0 if not specified.
func parseInValue(v string) (file string, line, col int, err error) {
	// Split from the right to handle Windows paths like C:\foo\bar.go:10:5.
	// The last two colon-separated segments might be line and col.
	parts := strings.Split(v, ":")

	// Try parsing from the end.
	n := len(parts)
	if n >= 3 {
		// Maybe file:line:col.
		c, errC := strconv.Atoi(parts[n-1])
		l, errL := strconv.Atoi(parts[n-2])
		if errC == nil && errL == nil && c > 0 && l > 0 {
			file = strings.Join(parts[:n-2], ":")
			return file, l, c, nil
		}
	}
	if n >= 2 {
		// Maybe file:line.
		l, errL := strconv.Atoi(parts[n-1])
		if errL == nil && l > 0 {
			file = strings.Join(parts[:n-1], ":")
			return file, l, 0, nil
		}
	}
	// Just a file path.
	return v, 0, 0, nil
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

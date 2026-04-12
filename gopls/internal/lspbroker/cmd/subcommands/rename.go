// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/format"
)

// RenameFlags holds the parsed flags for the "rename" subcommand.
type RenameFlags struct {
	// JSON requests --json output instead of formatted text.
	JSON bool

	// NoSpawn prevents daemon auto-spawn; fails with exit code 4 if the
	// broker is not already running.
	NoSpawn bool

	// Timeout is the wall-clock limit for the whole invocation.
	Timeout time.Duration

	// DryRun, if true, computes and prints the rename plan without writing
	// any files to disk.
	DryRun bool
}

// RunRename implements `gopls lspcli rename SYMBOL --in FILE[:line] --to NEWNAME [--dry-run]`.
//
// The CLI shape follows the existing discriminated form (ADR-007/008):
//
//   - rename SYMBOL --in FILE                name-based, file-scoped
//   - rename SYMBOL --in FILE:LINE           name-based, line-narrowed
//   - rename        --in FILE:LINE:COL --to NEWNAME  positional bypass
//
// --in and --to are always required.
//
// It returns (exitCode, error):
//
//	0  success
//	1  success but no changes (server returned empty edit)
//	2  user error (bad args, missing --in/--to)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunRename(ctx context.Context, args []string, flags RenameFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	symbol, inValue, toValue, dryRun, err := parseRenameArgs(args)
	if err != nil {
		return 2, err
	}
	if flags.DryRun {
		dryRun = true
	}

	if toValue == "" {
		return 2, fmt.Errorf("error: --to <newname> is required\n  usage: rename SYMBOL --in <file>[:line] --to <newname>")
	}

	file, line, col, err := parseInValue(inValue)
	if err != nil {
		return 2, err
	}

	// Validate mutual exclusion: symbol + full file:line:col is an error.
	if symbol != "" && col > 0 {
		return 2, fmt.Errorf("cannot specify both <symbol> and file:line:col; use one form:\n  rename <symbol> --in <file>[:line] --to <newname>\n  rename          --in <file>:<line>:<col> --to <newname>")
	}

	// Validate: positional bypass requires line and col.
	if symbol == "" && col == 0 {
		return 2, fmt.Errorf("positional form requires file:line:col, got %q\n  usage: rename --in <file>:<line>:<col> --to <newname>", inValue)
	}

	absFile, err := resolveFile(file)
	if err != nil {
		return 2, err
	}

	params := lspbroker.RenameParams{
		Version: lspbroker.ProtocolVersion,
		File:    absFile,
		NewName: toValue,
		DryRun:  dryRun,
	}
	if symbol != "" {
		params.Symbol = symbol
		if line > 0 {
			params.Line = line
		}
	} else {
		params.Line = line
		params.Character = lspbroker.IntPtr(col)
	}

	timeout := flags.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if !flags.NoSpawn {
		if err := EnsureDaemonRunning(ctx, cacheDir, selfPath); err != nil {
			return 4, fmt.Errorf("start broker daemon: %w", err)
		}
	}

	bc, err := DialBroker(ctx, cacheDir, selfPath)
	if err != nil {
		if flags.NoSpawn {
			return 4, fmt.Errorf("broker not running (--no-spawn): %w", err)
		}
		return 4, fmt.Errorf("connect to broker: %w", err)
	}
	defer bc.Close()

	result, err := bc.Rename(ctx, params)
	if err != nil {
		code := classifyError(err)
		return code, err
	}

	if result == nil || len(result.Changes) == 0 {
		return 1, nil // empty result
	}

	if flags.JSON {
		if err := format.RenameJSON(w, result); err != nil {
			return 4, err
		}
	} else {
		format.Rename(w, result)
	}
	return 0, nil
}

// parseRenameArgs extracts (symbol, inValue, toValue, dryRun) from CLI args.
// --in and --to are required; --dry-run is optional.
func parseRenameArgs(args []string) (symbol, inValue, toValue string, dryRun bool, err error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--in":
			if i+1 >= len(args) {
				return "", "", "", false, fmt.Errorf("--in requires a value")
			}
			inValue = args[i+1]
			i++
		case strings.HasPrefix(arg, "--in="):
			inValue = strings.TrimPrefix(arg, "--in=")
		case arg == "--to":
			if i+1 >= len(args) {
				return "", "", "", false, fmt.Errorf("--to requires a value")
			}
			toValue = args[i+1]
			i++
		case strings.HasPrefix(arg, "--to="):
			toValue = strings.TrimPrefix(arg, "--to=")
		case arg == "--dry-run":
			dryRun = true
		case strings.HasPrefix(arg, "-"):
			return "", "", "", false, fmt.Errorf("unknown flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}

	if inValue == "" {
		return "", "", "", false, fmt.Errorf("error: --in <file> is required\n  usage: rename SYMBOL --in <file>[:line] --to <newname>")
	}

	switch len(positional) {
	case 0:
		// Positional bypass form — no symbol.
	case 1:
		symbol = positional[0]
	default:
		return "", "", "", false, fmt.Errorf("too many positional arguments; expected at most 1 (symbol), got %d", len(positional))
	}

	return symbol, inValue, toValue, dryRun, nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker/format"
)

// RunSymbols implements `gopls lspcli symbols FILE`.
//
// It returns (exitCode, error):
//
//	0  success, non-empty result
//	1  success, empty result (no symbols found)
//	2  user error (bad args, file not found)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunSymbols(ctx context.Context, args []string, flags DefFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	// Parse args: expect exactly one positional FILE argument.
	if len(args) == 0 {
		return 2, fmt.Errorf("error: FILE is required\n  usage: symbols FILE")
	}
	if len(args) > 1 {
		return 2, fmt.Errorf("too many arguments; expected FILE, got %d args", len(args))
	}
	fileArg := args[0]

	absFile, err := resolveFile(fileArg)
	if err != nil {
		return 2, err
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

	raw, err := bc.DocumentSymbol(ctx, absFile)
	if err != nil {
		code := classifyError(err)
		return code, err
	}

	var hasContent bool
	if flags.JSON {
		hasContent, err = format.SymbolsJSON(w, raw)
	} else {
		hasContent, err = format.Symbols(w, raw)
	}
	if err != nil {
		return 4, err
	}
	if !hasContent {
		return 1, nil
	}
	return 0, nil
}

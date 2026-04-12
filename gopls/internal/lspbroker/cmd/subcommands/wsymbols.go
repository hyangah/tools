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

// RunWsymbols implements `gopls lspcli wsymbols QUERY`.
//
// It returns (exitCode, error):
//
//	0  success, non-empty result
//	1  success, empty result (no symbols found)
//	2  user error (bad args)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunWsymbols(ctx context.Context, args []string, flags DefFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	// Parse args: expect exactly one positional QUERY argument.
	if len(args) == 0 {
		return 2, fmt.Errorf("error: QUERY is required\n  usage: wsymbols QUERY")
	}
	if len(args) > 1 {
		return 2, fmt.Errorf("too many arguments; expected QUERY, got %d args", len(args))
	}
	query := args[0]

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

	raw, err := bc.WorkspaceSymbol(ctx, query)
	if err != nil {
		code := classifyError(err)
		return code, err
	}

	// wsymbols always outputs JSON.
	hasContent, err := format.SymbolsJSON(w, raw)
	if err != nil {
		return 4, err
	}
	if !hasContent {
		return 1, nil
	}
	return 0, nil
}

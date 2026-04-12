// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/format"
)

// RunRefs implements `gopls lspcli refs [<symbol>] --in <file>[:line[:col]]`.
//
// It returns (exitCode, error):
//
//	0  success, non-empty result
//	1  success, empty result (no references found)
//	2  user error (bad args, file not found, missing --in)
//	3  LSP error (server returned error, timeout)
//	4  broker error (daemon unreachable, protocol mismatch)
func RunRefs(ctx context.Context, args []string, flags DefFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	symbol, inValue, err := parseDefArgs(args)
	if err != nil {
		return 2, err
	}

	file, line, col, err := parseInValue(inValue)
	if err != nil {
		return 2, err
	}

	if symbol != "" && col > 0 {
		return 2, fmt.Errorf("cannot specify both <symbol> and file:line:col; use one form:\n  refs <symbol> --in <file>[:line]\n  refs          --in <file>:<line>:<col>")
	}

	if symbol == "" && col == 0 {
		return 2, fmt.Errorf("positional form requires file:line:col, got %q\n  usage: refs --in <file>:<line>:<col>", inValue)
	}

	absFile, err := resolveFile(file)
	if err != nil {
		return 2, err
	}

	params := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    absFile,
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

	locs, err := bc.References(ctx, params)
	if err != nil {
		code := classifyError(err)
		return code, err
	}

	if len(locs) == 0 {
		return 1, nil
	}

	if flags.JSON {
		if err := format.ReferencesJSON(w, locs); err != nil {
			return 4, err
		}
	} else {
		format.References(w, locs)
	}
	return 0, nil
}

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
)

// SyncFlags holds the parsed flags for the "sync" subcommand.
type SyncFlags struct {
	// NoSpawn prevents daemon auto-spawn.
	NoSpawn bool

	// Timeout is the wall-clock limit for the whole invocation.
	Timeout time.Duration
}

// RunSync implements `gopls lspcli sync FILE`.
//
// It tells the broker to re-read FILE from disk and send didChange to
// the language server, then clears stale diagnostics for the file.
//
// Exit codes:
//
//	0  success
//	2  user error (bad args, file not found)
//	3  LSP error
//	4  broker error
func RunSync(ctx context.Context, args []string, flags SyncFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("error: FILE is required\n  usage: sync FILE")
	}
	if len(args) > 1 {
		return 2, fmt.Errorf("too many arguments; expected exactly one FILE")
	}

	absFile, err := resolveFile(args[0])
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

	params := lspbroker.SyncParams{
		Version: lspbroker.ProtocolVersion,
		File:    absFile,
	}

	if err := bc.Sync(ctx, params); err != nil {
		return classifyError(err), err
	}

	fmt.Fprintf(w, "synced %s\n", absFile)
	return 0, nil
}

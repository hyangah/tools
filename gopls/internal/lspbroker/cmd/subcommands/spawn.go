// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// EnsureDaemonRunning checks whether the broker daemon is already running
// and, if not, spawns it. It returns nil once the daemon socket is
// confirmed connectable.
//
// cacheDir is the broker cache directory, typically [lspbroker.CacheRoot].
// selfPath is the absolute path to the currently running gopls binary
// (os.Executable()); it is re-invoked as "selfPath lspbrokerd --detach
// --cache-dir cacheDir" to start the daemon.
//
// EnsureDaemonRunning respects ctx for the polling loop but always
// issues at least one connection attempt before checking ctx.Done.
func EnsureDaemonRunning(ctx context.Context, cacheDir, selfPath string) error {
	sockPath := filepath.Join(cacheDir, "broker.sock")

	// Quick path: socket already exists and is connectable.
	if isSocketRunning(sockPath) {
		return nil
	}

	// Spawn the daemon. "selfPath lspbrokerd --detach" forks a background
	// process and then blocks for up to 2 s waiting for broker.sock to
	// appear, so when the command returns the daemon should be ready.
	cmd := exec.CommandContext(ctx, selfPath, "lspbrokerd", "--detach", "--cache-dir", cacheDir)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("spawn broker daemon: %w", err)
	}

	// Verify the socket is connectable (with a short retry in case
	// the daemon is still binding).
	const (
		pollInterval = 50 * time.Millisecond
		pollTimeout  = 3 * time.Second
	)
	deadline := time.Now().Add(pollTimeout)
	for !time.Now().After(deadline) {
		if isSocketRunning(sockPath) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("broker daemon socket %s did not appear after %v", sockPath, pollTimeout)
}

// isSocketRunning returns true if sockPath refers to a unix socket that
// accepts connections. It dials with a short timeout so it returns quickly
// even if the socket file exists but nobody is listening.
func isSocketRunning(sockPath string) bool {
	if _, err := os.Stat(sockPath); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

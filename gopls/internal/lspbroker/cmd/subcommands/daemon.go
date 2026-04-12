// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

// RunDaemon dispatches the "gopls lspcli daemon" subcommand to one of
// start, stop, status, or restart.
func RunDaemon(ctx context.Context, args []string, cacheDir, selfPath string, w io.Writer) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("daemon: must provide action (start|stop|status|restart)")
	}
	switch args[0] {
	case "start":
		return daemonStart(ctx, cacheDir, selfPath, w)
	case "stop":
		return daemonStop(ctx, cacheDir, selfPath, w)
	case "status":
		return daemonStatus(ctx, cacheDir, selfPath, w)
	case "restart":
		return daemonRestart(ctx, cacheDir, selfPath, w)
	default:
		return 2, fmt.Errorf("daemon: unknown action %q (want start|stop|status|restart)", args[0])
	}
}

func daemonStart(ctx context.Context, cacheDir, selfPath string, w io.Writer) (int, error) {
	sockPath := filepath.Join(cacheDir, "broker.sock")
	if isSocketRunning(sockPath) {
		pidPath := filepath.Join(cacheDir, "broker.pid")
		pid, err := lspbroker.ReadPIDFile(pidPath)
		if err == nil {
			fmt.Fprintf(w, "daemon already running (pid %d)\n", pid)
		} else {
			fmt.Fprintln(w, "daemon already running")
		}
		return 0, nil
	}
	if err := EnsureDaemonRunning(ctx, cacheDir, selfPath); err != nil {
		return 1, fmt.Errorf("daemon start: %w", err)
	}
	fmt.Fprintln(w, "daemon started")
	return 0, nil
}

func daemonStop(ctx context.Context, cacheDir, selfPath string, w io.Writer) (int, error) {
	sockPath := filepath.Join(cacheDir, "broker.sock")
	if !isSocketRunning(sockPath) {
		fmt.Fprintln(w, "daemon not running")
		return 0, nil
	}

	// Try graceful stop via broker.stop RPC.
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	bc, err := DialBroker(stopCtx, cacheDir, selfPath)
	if err != nil {
		// Can't connect — try SIGTERM via pidfile.
		return daemonKill(cacheDir, w)
	}
	defer bc.Close()

	if err := bc.Stop(stopCtx); err != nil {
		// RPC failed — fall back to SIGTERM.
		return daemonKill(cacheDir, w)
	}

	// Wait for socket to disappear (daemon exiting).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !isSocketRunning(sockPath) {
			fmt.Fprintln(w, "daemon stopped")
			return 0, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Socket still there — try SIGTERM.
	return daemonKill(cacheDir, w)
}

func daemonKill(cacheDir string, w io.Writer) (int, error) {
	pidPath := filepath.Join(cacheDir, "broker.pid")
	pid, err := lspbroker.ReadPIDFile(pidPath)
	if err != nil {
		fmt.Fprintln(w, "daemon stopped (no pidfile)")
		return 0, nil
	}
	if !lspbroker.IsPIDAlive(pid) {
		fmt.Fprintln(w, "daemon stopped (stale pidfile)")
		return 0, nil
	}
	if err := killProcess(pid); err != nil {
		return 1, fmt.Errorf("daemon stop: kill pid %d: %w", pid, err)
	}
	fmt.Fprintf(w, "daemon stopped (killed pid %d)\n", pid)
	return 0, nil
}

func daemonStatus(ctx context.Context, cacheDir, selfPath string, w io.Writer) (int, error) {
	sockPath := filepath.Join(cacheDir, "broker.sock")
	if !isSocketRunning(sockPath) {
		fmt.Fprintln(w, "not running")
		return 1, nil
	}

	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	bc, err := DialBroker(statusCtx, cacheDir, selfPath)
	if err != nil {
		fmt.Fprintln(w, "not running (socket exists but handshake failed)")
		return 1, nil
	}
	defer bc.Close()

	// The handshake response is returned by DialBroker internally.
	// Reconnect to get status info. For now, report basic info from pidfile.
	pidPath := filepath.Join(cacheDir, "broker.pid")
	pid, _ := lspbroker.ReadPIDFile(pidPath)
	fmt.Fprintf(w, "running (pid %d)\n", pid)
	return 0, nil
}

func daemonRestart(ctx context.Context, cacheDir, selfPath string, w io.Writer) (int, error) {
	code, err := daemonStop(ctx, cacheDir, selfPath, w)
	if err != nil {
		return code, err
	}
	// Brief pause for socket cleanup.
	time.Sleep(200 * time.Millisecond)
	return daemonStart(ctx, cacheDir, selfPath, w)
}

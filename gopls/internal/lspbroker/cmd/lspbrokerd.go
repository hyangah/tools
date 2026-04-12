// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/goadapter"
	goplsversion "golang.org/x/tools/gopls/internal/version"
)

// RunLSPBrokerd is the entry point for `gopls lspbrokerd`. It runs
// the long-lived broker daemon.
//
// Flags are parsed by gopls's tool framework before this function is
// called and passed in as explicit parameters:
//
//   - detach: fork a background process and exit immediately.
//   - cacheDir: override the cache directory (empty = use CacheRoot()).
func RunLSPBrokerd(ctx context.Context, detach bool, cacheDir string) error {
	if cacheDir == "" {
		cacheDir = lspbroker.CacheRoot()
	}

	if detach {
		return detachDaemon(cacheDir)
	}

	return runDaemon(ctx, cacheDir)
}

// runDaemon starts the broker daemon in the foreground: creates the
// cache directory, acquires the pidfile, opens the socket listener,
// and calls Broker.Serve. It returns when the context is cancelled or
// the listener is closed.
func runDaemon(ctx context.Context, cacheDir string) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("lspbrokerd: create cache dir %s: %w", cacheDir, err)
	}

	pidPath := filepath.Join(cacheDir, "broker.pid")
	releasePID, err := lspbroker.AcquirePIDFile(pidPath)
	if err != nil {
		return fmt.Errorf("lspbrokerd: %w", err)
	}
	defer releasePID()

	l, err := lspbroker.NewListener(cacheDir)
	if err != nil {
		return fmt.Errorf("lspbrokerd: %w", err)
	}
	defer l.Close()

	self, _ := os.Executable()
	b := lspbroker.NewBroker(self, goplsversion.Version())
	b.GoSessionFactory = func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}

	// Idle timeout: default 30 minutes, overridable via env.
	idleTimeout := 30 * time.Minute
	if s := os.Getenv("LSP_BROKER_IDLE_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("lspbrokerd: parse LSP_BROKER_IDLE_TIMEOUT=%q: %w", s, err)
		}
		idleTimeout = d
	}
	b.IdleTimeout = idleTimeout

	fmt.Fprintf(os.Stderr, "lspbrokerd: listening on %s (idle timeout %v)\n", l.Addr(), idleTimeout)
	return b.Serve(ctx, l)
}

// PrintLSPBrokerdHelp writes the lspbrokerd long-form help to w. It
// is invoked by the tool.Application wrapper's DetailedHelp method
// in gopls/internal/cmd/lspbroker.go.
func PrintLSPBrokerdHelp(w io.Writer) {
	fmt.Fprint(w, `
lspbrokerd is the long-lived broker daemon for the LSP broker. It
is normally spawned automatically by "gopls lspcli" on first use
and rarely invoked by hand.

The daemon manages LSP server subprocesses on behalf of shell-based
AI agents, listens on a per-user unix domain socket under
$XDG_CACHE_HOME/lsp-broker/<buildid>/, and idles itself out after a
configurable timeout.

Flags:
  -detach        run daemon in background and exit (used by lspcli auto-spawn)
  -cache-dir D   override the cache directory (default: auto from build-id)
`)
}

// detachDaemon re-executes the current binary as a background daemon
// without the -detach flag, waits up to 2 s for broker.sock to
// appear, then returns nil so the parent (the original lspcli call)
// can proceed.
//
// The detach mechanism is OS-specific; see daemon_posix.go.
func detachDaemon(cacheDir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("lspbrokerd -detach: resolve executable: %w", err)
	}
	if err := spawnDetached(self, cacheDir); err != nil {
		return fmt.Errorf("lspbrokerd -detach: spawn: %w", err)
	}
	// Wait up to 2 s for the socket to appear.
	sockPath := filepath.Join(cacheDir, "broker.sock")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Socket never appeared; return nil anyway — the CLI will detect
	// the missing socket on its own and report an error there.
	return nil
}

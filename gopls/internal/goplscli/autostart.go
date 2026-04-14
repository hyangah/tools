// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package goplscli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
)

// DefaultSocketAddress returns the well-known socket path for the CLI
// protocol, derived from the current binary's build-id (same hash as LSP).
// The returned string has the form "/path/to/gopls-{hash}-cli.{user}".
func DefaultSocketAddress() (string, error) {
	goplsPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("getting executable path: %w", err)
	}
	return cliSocketPath(goplsPath), nil
}

// cliSocketPath computes the deterministic CLI socket path for a given
// gopls binary path, using the same hash derivation as lsprpc's
// autoNetworkAddressPosix.
func cliSocketPath(goplsPath string) string {
	shortHash := binaryHash(goplsPath)
	user := os.Getenv("USER")
	if user == "" {
		user = "shared"
	}
	basename := filepath.Base(goplsPath)
	runtimeDir := os.TempDir()
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		runtimeDir = xdg
	}
	return filepath.Join(runtimeDir, fmt.Sprintf("%s-%s-cli.%s", basename, shortHash, user))
}

// lspSocketPath computes the LSP daemon socket path for a given gopls
// binary path, matching lsprpc's autoNetworkAddressPosix naming.
func lspSocketPath(goplsPath string) string {
	shortHash := binaryHash(goplsPath)
	user := os.Getenv("USER")
	if user == "" {
		user = "shared"
	}
	basename := filepath.Base(goplsPath)
	runtimeDir := os.TempDir()
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		runtimeDir = xdg
	}
	return filepath.Join(runtimeDir, fmt.Sprintf("%s-%s-daemon.%s", basename, shortHash, user))
}

// binaryHash returns a short hex hash identifying this binary build.
//
// It first tries debug.ReadBuildInfo (in-process, ~0 cost). If that
// fails, it falls back to hashing the binary path.
//
// Note: lsprpc's autoNetworkAddressPosix uses `go tool buildid` which
// spawns a subprocess (~90ms). We avoid that here because the CLI calls
// this on every invocation. The hashes won't match lsprpc's, but they
// don't need to — CLI and LSP use different socket name suffixes
// ("-cli." vs "-daemon.").
func binaryHash(goplsPath string) string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		// Hash the full build info string — it includes Go version,
		// module versions, and build settings (vcs.revision, etc.).
		sum := sha256.Sum256([]byte(bi.String()))
		return fmt.Sprintf("%x", sum[:])[:6]
	}
	log.Printf("debug.ReadBuildInfo unavailable, falling back to path hash")
	sum := sha256.Sum256([]byte(goplsPath))
	return fmt.Sprintf("%x", sum[:])[:6]
}

// AutoConnect connects to the CLI daemon at the default socket address.
// If the daemon is not running, it spawns "gopls serve" with the
// appropriate --cli.listen and --listen flags and retries the connection.
// It returns the address suitable for passing to [SendRequest].
func AutoConnect(ctx context.Context) (string, error) {
	goplsPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("getting executable path: %w", err)
	}

	cliAddr := cliSocketPath(goplsPath)

	// Try connecting once — the daemon may already be running.
	const dialTimeout = 1 * time.Second
	conn, err := net.DialTimeout("unix", cliAddr, dialTimeout)
	if err == nil {
		conn.Close()
		return cliAddr, nil
	}

	// Daemon is not running. Remove stale socket file if present.
	if _, statErr := os.Stat(cliAddr); statErr == nil {
		if removeErr := os.Remove(cliAddr); removeErr != nil {
			return "", fmt.Errorf("removing stale CLI socket: %w", removeErr)
		}
	}

	// Also remove stale LSP socket if present. The spawned daemon needs
	// to bind this address; if a previous daemon crashed, the socket file
	// is left behind and net.Listen will fail.
	lspAddr := lspSocketPath(goplsPath)
	if _, statErr := os.Stat(lspAddr); statErr == nil {
		if removeErr := os.Remove(lspAddr); removeErr != nil {
			return "", fmt.Errorf("removing stale LSP socket: %w", removeErr)
		}
	}

	// Spawn "gopls serve --listen=unix;<lsp-sock> --cli.listen=<cli-sock>".
	// The --listen flag keeps the daemon alive as a proper LSP listener;
	// without it, the stdio-based LSP server would exit immediately when
	// backgrounded (EOF on stdin).
	cmd := exec.Command(goplsPath, "serve",
		"-listen", fmt.Sprintf("unix;%s", lspAddr),
		"-cli.listen", cliAddr,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	// Disconnect stdio so the daemon doesn't hold our stdin/stdout.
	cmd.Stdin = nil
	cmd.Stdout = nil
	f, _ := os.Create("/tmp/gopls-daemon.log")
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting gopls daemon: %w", err)
	}
	// Detach — we don't wait for the daemon.
	go cmd.Wait() //nolint: errcheck

	// Retry connecting. The daemon needs time to bind the socket.
	const retries = 5
	for i := range retries {
		startDial := time.Now()
		conn, err = net.DialTimeout("unix", cliAddr, dialTimeout)
		if err == nil {
			conn.Close()
			return cliAddr, nil
		}
		if i < retries-1 {
			// Ensure we wait at least dialTimeout before retrying.
			time.Sleep(dialTimeout - time.Since(startDial))
		}
	}
	return "", fmt.Errorf("connecting to gopls CLI daemon after auto-start: %w", err)
}

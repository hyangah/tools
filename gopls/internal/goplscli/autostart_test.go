// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package goplscli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSocketAddress(t *testing.T) {
	addr, err := DefaultSocketAddress()
	if err != nil {
		t.Fatalf("DefaultSocketAddress() error: %v", err)
	}
	if addr == "" {
		t.Fatal("DefaultSocketAddress() returned empty string")
	}
	if !strings.Contains(addr, "cli") {
		t.Errorf("DefaultSocketAddress() = %q, want path containing 'cli'", addr)
	}

	// Verify the path is under a reasonable directory.
	dir := filepath.Clean(filepath.Dir(addr))
	tmpDir := filepath.Clean(os.TempDir())
	xdgDir := os.Getenv("XDG_RUNTIME_DIR")
	if dir != tmpDir && (xdgDir == "" || dir != filepath.Clean(xdgDir)) {
		t.Errorf("DefaultSocketAddress() dir = %q, want %q or %q", dir, tmpDir, xdgDir)
	}
}

func TestCLISocketPathDeterministic(t *testing.T) {
	// Same binary path should always produce the same socket path.
	path1 := cliSocketPath("/usr/local/bin/gopls")
	path2 := cliSocketPath("/usr/local/bin/gopls")
	if path1 != path2 {
		t.Errorf("cliSocketPath not deterministic: %q != %q", path1, path2)
	}
}

func TestCLIAndLSPSocketPathsDiffer(t *testing.T) {
	cli := cliSocketPath("/usr/local/bin/gopls")
	lsp := lspSocketPath("/usr/local/bin/gopls")
	if cli == lsp {
		t.Errorf("CLI and LSP socket paths should differ, both = %q", cli)
	}
	if !strings.Contains(cli, "-cli.") {
		t.Errorf("CLI socket path should contain '-cli.', got %q", cli)
	}
	if !strings.Contains(lsp, "-daemon.") {
		t.Errorf("LSP socket path should contain '-daemon.', got %q", lsp)
	}
}

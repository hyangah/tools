// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIDHash_Stable(t *testing.T) {
	// Calling BuildIDHash twice in the same process should return
	// the same value — otherwise the CLI and a daemon it just
	// spawned would land on different socket paths.
	a := BuildIDHash()
	b := BuildIDHash()
	if a != b {
		t.Errorf("BuildIDHash is not stable: %q vs %q", a, b)
	}
	if len(a) != BuildIDHashLen {
		t.Errorf("BuildIDHash length = %d, want %d", len(a), BuildIDHashLen)
	}
}

func TestBuildIDHashFor_DistinctPaths(t *testing.T) {
	// Two distinct synthetic binary paths must produce distinct
	// hashes via the path-hash fallback. We can't easily force the
	// "go tool buildid" path to succeed for arbitrary synthetic
	// inputs (it rejects non-Go binaries), so we rely on the
	// fallback branch here, which is the pathological case anyway.
	dir := t.TempDir()
	path1 := filepath.Join(dir, "gopls-a")
	path2 := filepath.Join(dir, "gopls-b")
	if err := os.WriteFile(path1, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("also not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	h1 := buildIDHashFor(path1)
	h2 := buildIDHashFor(path2)
	if h1 == h2 {
		t.Errorf("distinct paths produced identical hash %q", h1)
	}
	if len(h1) != BuildIDHashLen || len(h2) != BuildIDHashLen {
		t.Errorf("unexpected hash lengths: %q %q", h1, h2)
	}
}

func TestBuildIDHashFor_SamePathStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gopls")
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	h1 := buildIDHashFor(path)
	h2 := buildIDHashFor(path)
	if h1 != h2 {
		t.Errorf("buildIDHashFor on same path is not stable: %q vs %q", h1, h2)
	}
}

func TestCacheRoot_EnvOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("LSP_BROKER_CACHE", override)

	got := CacheRoot()
	if got != override {
		t.Errorf("CacheRoot() = %q, want override %q", got, override)
	}
}

func TestCacheRoot_XDGCacheHome(t *testing.T) {
	// With LSP_BROKER_CACHE unset and XDG_CACHE_HOME set, the cache
	// root must be $XDG_CACHE_HOME/lsp-broker/<hash>.
	t.Setenv("LSP_BROKER_CACHE", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)

	got := CacheRoot()
	want := filepath.Join(xdg, "lsp-broker")
	if !strings.HasPrefix(got, want+string(filepath.Separator)) {
		t.Errorf("CacheRoot() = %q, want prefix %q", got, want+string(filepath.Separator))
	}
	rest := strings.TrimPrefix(got, want+string(filepath.Separator))
	if len(rest) != BuildIDHashLen {
		t.Errorf("cache-root suffix = %q (len %d), want %d-char build-id hash",
			rest, len(rest), BuildIDHashLen)
	}
}

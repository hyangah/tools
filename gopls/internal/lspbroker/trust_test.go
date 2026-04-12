// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTrustStore_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if ts.IsTrusted("/some/path") {
		t.Error("empty store: IsTrusted should return false")
	}
	if got := ts.List(); len(got) != 0 {
		t.Errorf("empty store: List() = %v, want []", got)
	}
}

func TestTrustStore_AddAndCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !ts.IsTrusted("/a/b") {
		t.Error("IsTrusted(/a/b) = false, want true")
	}
}

func TestTrustStore_PrefixMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if !ts.IsTrusted("/a/b/c/d") {
		t.Error("IsTrusted(/a/b/c/d) = false, want true (prefix match)")
	}
	if ts.IsTrusted("/a/bc") {
		t.Error("IsTrusted(/a/bc) = true, want false (not a path-boundary prefix)")
	}
}

func TestTrustStore_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !ts.IsTrusted("/a/b") {
		t.Error("IsTrusted(/a/b) = false, want true (exact match)")
	}
}

func TestTrustStore_Remove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ts.Remove("/a/b"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ts.IsTrusted("/a/b") {
		t.Error("IsTrusted(/a/b) = true after Remove, want false")
	}
}

func TestTrustStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/persist/me"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Load a new TrustStore from the same file.
	ts2, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore (second): %v", err)
	}
	if !ts2.IsTrusted("/persist/me") {
		t.Error("IsTrusted(/persist/me) = false after reload, want true")
	}
}

func TestTrustStore_DuplicateAdd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add (1st): %v", err)
	}
	if err := ts.Add("/a/b"); err != nil {
		t.Fatalf("Add (2nd): %v", err)
	}
	list := ts.List()
	if len(list) != 1 {
		t.Errorf("List() has %d entries after duplicate Add, want 1: %v", len(list), list)
	}
}

func TestTrustStore_List(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")

	ts, err := LoadTrustStore(path)
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}
	paths := []string{"/x/y", "/a/b", "/foo/bar"}
	for _, p := range paths {
		if err := ts.Add(p); err != nil {
			t.Fatalf("Add(%q): %v", p, err)
		}
	}
	list := ts.List()
	if len(list) != len(paths) {
		t.Errorf("List() returned %d entries, want %d: %v", len(list), len(paths), list)
	}
	// Check all paths are present.
	for _, p := range paths {
		if !slices.Contains(list, p) {
			t.Errorf("List() missing %q", p)
		}
	}
}

func TestDefaultTrustStorePath(t *testing.T) {
	p := DefaultTrustStorePath()
	want := filepath.Join("lsp-broker", "trusted.json")
	if !strings.HasSuffix(p, want) {
		t.Errorf("DefaultTrustStorePath() = %q, want suffix %q", p, want)
	}
}

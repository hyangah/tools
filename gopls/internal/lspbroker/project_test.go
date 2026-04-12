// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"os"
	"path/filepath"
	"testing"
)

// touch creates an empty file at path, creating parent directories as needed.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// mkdir creates a directory at path, creating parent directories as needed.
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFindProjectRoot_LspJson(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// dir/
	//   .lsp.json
	//   sub/
	//     file.ts
	touch(t, filepath.Join(dir, ".lsp.json"))
	touch(t, filepath.Join(dir, "sub", "file.ts"))

	got, err := FindProjectRoot(filepath.Join(dir, "sub", "file.ts"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("FindProjectRoot = %q, want %q", got, dir)
	}
}

func TestFindProjectRoot_GoMod(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// dir/
	//   go.mod
	//   pkg/
	//     file.go
	touch(t, filepath.Join(dir, "go.mod"))
	touch(t, filepath.Join(dir, "pkg", "file.go"))

	got, err := FindProjectRoot(filepath.Join(dir, "pkg", "file.go"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("FindProjectRoot = %q, want %q", got, dir)
	}
}

func TestFindProjectRoot_GoWork(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// dir/
	//   go.work         ← should win
	//   mod/
	//     go.mod
	//     pkg/
	//       file.go
	touch(t, filepath.Join(dir, "go.work"))
	touch(t, filepath.Join(dir, "mod", "go.mod"))
	touch(t, filepath.Join(dir, "mod", "pkg", "file.go"))

	got, err := FindProjectRoot(filepath.Join(dir, "mod", "pkg", "file.go"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("FindProjectRoot = %q (go.work dir), want %q", got, dir)
	}
}

func TestFindProjectRoot_Git(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// dir/
	//   .git/           ← directory
	//   src/
	//     main.py
	mkdir(t, filepath.Join(dir, ".git"))
	touch(t, filepath.Join(dir, "src", "main.py"))

	got, err := FindProjectRoot(filepath.Join(dir, "src", "main.py"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("FindProjectRoot = %q, want %q", got, dir)
	}
}

func TestFindProjectRoot_LspJsonPriority(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// parent/
	//   .lsp.json       ← should win
	//   child/
	//     go.mod
	//     file.go
	touch(t, filepath.Join(dir, ".lsp.json"))
	touch(t, filepath.Join(dir, "child", "go.mod"))
	touch(t, filepath.Join(dir, "child", "file.go"))

	got, err := FindProjectRoot(filepath.Join(dir, "child", "file.go"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("FindProjectRoot = %q, want %q (parent with .lsp.json)", got, dir)
	}
}

func TestFindProjectRoot_NotFound(t *testing.T) {
	resetProjectRootCache()
	// Use a directory under $HOME that has no sentinels. We create a deep
	// temp dir and ensure no sentinels exist in it.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	// Create a temp dir under home that we can fully control.
	// t.TempDir() creates under os.TempDir() which may be outside home;
	// so we create our own under home/tmp-lspbroker-test.
	testDir := filepath.Join(home, "tmp-lspbroker-testnotfound")
	subDir := filepath.Join(testDir, "a", "b", "c")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(testDir) })

	filePath := filepath.Join(subDir, "file.txt")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = FindProjectRoot(filePath)
	if err != ErrProjectNotFound {
		t.Errorf("FindProjectRoot = %v, want ErrProjectNotFound", err)
	}
}

func TestFindProjectRoot_NestedProject(t *testing.T) {
	resetProjectRootCache()
	dir := t.TempDir()

	// dir/
	//   .git/           ← outer git repo
	//   module/
	//     go.mod        ← nearer sentinel (priority 3 > 4), should win
	//     pkg/
	//       file.go
	mkdir(t, filepath.Join(dir, ".git"))
	touch(t, filepath.Join(dir, "module", "go.mod"))
	touch(t, filepath.Join(dir, "module", "pkg", "file.go"))

	got, err := FindProjectRoot(filepath.Join(dir, "module", "pkg", "file.go"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	// go.mod is priority 3, .git is priority 4 — go.mod wins regardless of distance.
	wantDir := filepath.Join(dir, "module")
	if got != wantDir {
		t.Errorf("FindProjectRoot = %q, want %q (nearest go.mod)", got, wantDir)
	}
}

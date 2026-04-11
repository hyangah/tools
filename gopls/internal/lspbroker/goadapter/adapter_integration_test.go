// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/goadapter"
)

// TestGoSession_Definition tests the full goadapter path:
//   - spawns a gopls serve subprocess
//   - sends textDocument/definition for a call site in main.go
//   - asserts the result points to the function definition in lib.go
//
// The fixture project lives at testdata/foo/ but is copied to a
// t.TempDir() so that gopls does not skip it as a testdata directory.
func TestGoSession_Definition(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Copy the fixture project to a temp directory so gopls doesn't
	// treat it as a build-ignored testdata directory.
	// The goadapter package is at .../lspbroker/goadapter/; testdata is at
	// .../lspbroker/testdata/.
	fixtureDir := filepath.Join("..", "testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	mainGo := filepath.Join(tmpDir, "main.go")
	libGo := filepath.Join(tmpDir, "lib.go")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess := goadapter.NewGoSession(tmpDir)
	defer sess.Close()

	// Request definition for Greeting at line 11, col 9 (1-based).
	// See testdata/foo/main.go for the exact position comment.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: 9,
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	rawResult, err := sess.Handle(ctx, lspbroker.DefinitionMethod, rawParams)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var locs []lspbroker.Location
	if err := json.Unmarshal(rawResult, &locs); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(locs) == 0 {
		t.Fatal("expected at least one definition location, got none")
	}

	t.Logf("got %d location(s):", len(locs))
	for i, l := range locs {
		t.Logf("  [%d] URI=%s line=%d char=%d", i, l.URI, l.Range.Start.Line, l.Range.Start.Character)
	}

	// Verify the result points to lib.go.
	got := locs[0]
	gotPath := uriToPath(got.URI)
	if gotPath != libGo {
		// Allow relative-path matches too.
		if !strings.HasSuffix(gotPath, "lib.go") {
			t.Errorf("definition URI path = %q, want %q or suffix lib.go", gotPath, libGo)
		}
	}

	// func Greeting is defined at line 5 col 6 (1-based) in lib.go;
	// the broker returns 0-based values: line=4, char=5.
	if got.Range.Start.Line != 4 {
		t.Errorf("result start line = %d (0-based), want 4 (= lib.go line 5)", got.Range.Start.Line)
	}
	if got.Range.Start.Character != 5 {
		t.Errorf("result start character = %d (0-based), want 5 (= lib.go col 6, after 'func ')", got.Range.Start.Character)
	}
}

// copyDir recursively copies the contents of src into dst (which must exist).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// copyFile copies the file at src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// uriToPath strips the file:// prefix from a URI.
func uriToPath(uri string) string {
	if s, ok := strings.CutPrefix(uri, "file://"); ok {
		return s
	}
	return uri
}

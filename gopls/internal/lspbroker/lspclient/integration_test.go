// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker/lspclient"
	"golang.org/x/tools/gopls/internal/protocol"
)

// TestIntegration_Definition spawns a real gopls server and sends a
// textDocument/definition request against the test fixture project.
// It is skipped if gopls is not on PATH.
func TestIntegration_Definition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	goplsPath, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH: skipping integration test")
	}

	// Locate the fixture project. The test binary's source file is at
	// gopls/internal/lspbroker/lspclient/, and the fixture is at
	// gopls/internal/lspbroker/testdata/gomod/.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// thisFile: .../gopls/internal/lspbroker/lspclient/integration_test.go
	lspclientDir := filepath.Dir(thisFile)
	fixtureDir := filepath.Join(lspclientDir, "..", "testdata", "gomod")
	fixtureDir, err = filepath.Abs(fixtureDir)
	if err != nil {
		t.Fatalf("abs fixture dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixtureDir, "go.mod")); err != nil {
		t.Skipf("fixture dir %s not found: %v", fixtureDir, err)
	}

	mainGo := filepath.Join(fixtureDir, "main.go")
	rootURI := string(protocol.URIFromPath(fixtureDir))
	mainURI := string(protocol.URIFromPath(mainGo))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := lspclient.Config{
		Command:        []string{goplsPath, "serve"},
		RootURI:        rootURI,
		StartupTimeout: 30 * time.Second,
	}

	c, err := lspclient.Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Shutdown(shutCtx); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	if err := c.EnsureOpen(ctx, mainGo, "go"); err != nil {
		t.Fatalf("EnsureOpen: %v", err)
	}

	// In main.go:
	//   line 14 (0-based): msg := greet("world")
	//                                ^5  — calling greet
	// We request definition of "greet" at line=14 (0-indexed), char=8
	// (pointing inside the identifier "greet").
	// The expected result should be the definition site on line 9:
	//   func greet(name string) string {
	locs, err := c.Definition(ctx, mainURI, 14, 8)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definition returned no locations")
	}
	t.Logf("Definition result: %+v", locs[0])

	// Verify the result points into main.go at the greet function definition.
	if string(locs[0].URI) != mainURI {
		t.Errorf("Definition URI = %q, want %q", locs[0].URI, mainURI)
	}
	// The definition of greet is on line 9 (0-based).
	wantLine := uint32(9)
	if locs[0].Range.Start.Line != wantLine {
		t.Errorf("Definition line = %d, want %d", locs[0].Range.Start.Line, wantLine)
	}
}

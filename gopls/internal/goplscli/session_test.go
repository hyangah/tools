// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
)

// newTestSession creates a ServerSession for a test module at the given root.
func newTestSession(t *testing.T, ctx context.Context, c *cache.Cache, root string) *goplscli.ServerSession {
	t.Helper()
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}
	t.Cleanup(func() { gs.Close(ctx) })
	return gs
}

func TestServerSessionDefinition(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	uri := protocol.URIFromPath(mainFile)
	// "Greeting" call at line 10 (0-based), char 8
	rng := protocol.Range{
		Start: protocol.Position{Line: 10, Character: 8},
		End:   protocol.Position{Line: 10, Character: 8},
	}
	locs, err := gs.Definition(ctx, uri, rng)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definition returned no results")
	}
	if locs[0].Range.Start.Line != 5 {
		t.Errorf("expected definition at line 5 (0-based), got %d", locs[0].Range.Start.Line)
	}
}

func TestServerSessionReferences(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	for _, f := range []string{"main.go", "main_test.go"} {
		if err := gs.EnsureSynced(ctx, filepath.Join(root, f)); err != nil {
			t.Fatalf("EnsureSynced(%s): %v", f, err)
		}
	}

	uri := protocol.URIFromPath(filepath.Join(root, "main.go"))
	rng := protocol.Range{
		Start: protocol.Position{Line: 5, Character: 5},
		End:   protocol.Position{Line: 5, Character: 5},
	}
	refs, err := gs.References(ctx, uri, rng, true)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) < 2 {
		t.Errorf("expected at least 2 references, got %d", len(refs))
	}
}

func TestServerSessionHover(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	uri := protocol.URIFromPath(mainFile)
	rng := protocol.Range{
		Start: protocol.Position{Line: 5, Character: 5},
		End:   protocol.Position{Line: 5, Character: 5},
	}
	hover, err := gs.Hover(ctx, uri, rng)
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	t.Logf("Hover: %s", hover.Contents.Value)
}

func TestServerSessionDocumentSymbols(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	uri := protocol.URIFromPath(mainFile)
	symbols, err := gs.DocumentSymbols(ctx, uri)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(symbols) < 2 {
		t.Errorf("expected at least 2 symbols, got %d", len(symbols))
	}
}

func TestServerSessionDiagnoseFile(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	uri := protocol.URIFromPath(mainFile)
	diags, err := gs.DiagnoseFile(ctx, uri)
	if err != nil {
		t.Fatalf("DiagnoseFile: %v", err)
	}
	// Valid Go code should have no errors.
	for _, d := range diags {
		t.Logf("diagnostic: %s: %s", d.Source, d.Message)
	}
}

func TestServerSessionEnsureSyncedFingerprint(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")

	// First sync — should open.
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("first EnsureSynced: %v", err)
	}

	// Second sync with same content — should be a no-op (fingerprint match).
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("second EnsureSynced: %v", err)
	}

	// Modify the file.
	if err := os.WriteFile(mainFile, []byte(`package main

func main() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Third sync — fingerprint changed, should re-read.
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("third EnsureSynced: %v", err)
	}

	// Verify the change is visible.
	uri := protocol.URIFromPath(mainFile)
	symbols, err := gs.DocumentSymbols(ctx, uri)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	// Only "main" function should remain.
	if len(symbols) != 1 {
		t.Errorf("expected 1 symbol after modification, got %d", len(symbols))
	}
}

// TestServerSessionEnsureSyncedMissingFile verifies that EnsureSynced
// returns an error when the file does not exist on disk.
func TestServerSessionEnsureSyncedMissingFile(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	missingFile := filepath.Join(root, "nonexistent.go")
	err := gs.EnsureSynced(ctx, missingFile)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	t.Logf("EnsureSynced error (expected): %v", err)
}

// TestServerSessionConcurrent verifies that multiple goroutines can use
// the same GoSession concurrently without races.
func TestServerSessionConcurrent(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	gs := newTestSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatal(err)
	}

	uri := protocol.URIFromPath(mainFile)
	rng := protocol.Range{
		Start: protocol.Position{Line: 5, Character: 5},
		End:   protocol.Position{Line: 5, Character: 5},
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := gs.Definition(ctx, uri, rng)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Definition: %v", err)
	}
}

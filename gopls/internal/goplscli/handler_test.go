// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
)

func TestCLIHandlerSessionFor(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	gs, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatalf("SessionFor: %v", err)
	}
	if gs == nil {
		t.Fatal("SessionFor returned nil")
	}
	if gs.Root() != root {
		t.Errorf("Root() = %q, want %q", gs.Root(), root)
	}

	// Second call should return the same session (cached).
	gs2, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatalf("SessionFor (second): %v", err)
	}
	if gs2 != gs {
		t.Error("SessionFor returned a different session for the same root")
	}
}

func TestCLIHandlerSessionForFile(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	mainFile := filepath.Join(root, "main.go")
	gs, err := h.SessionForFile(ctx, mainFile)
	if err != nil {
		t.Fatalf("SessionForFile: %v", err)
	}
	if gs.Root() != root {
		t.Errorf("Root() = %q, want %q", gs.Root(), root)
	}
}

func TestCLIHandlerConcurrentSessionFor(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	// Multiple goroutines requesting the same root should all get
	// the same session.
	var wg sync.WaitGroup
	sessions := make([]*goplscli.ServerSession, 5)
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gs, err := h.SessionFor(ctx, root)
			sessions[idx] = gs
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: SessionFor: %v", i, err)
		}
	}
	// All should be the same session.
	for i := 1; i < len(sessions); i++ {
		if sessions[i] != sessions[0] {
			t.Errorf("goroutine %d got a different session", i)
		}
	}
}

func TestCLIHandlerEvictSession(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	gs1, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	h.EvictSession(ctx, root, time.Time{}) // force eviction

	gs2, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if gs2 == gs1 {
		t.Error("after eviction, SessionFor returned the same session")
	}
}

// TestCLIHandlerEvictSessionCutoffRace verifies that EvictSession does not
// evict a session that was accessed after the cutoff time. This tests the
// race fix where evictIdle scans stale roots, but by the time EvictSession
// runs, the session may have been accessed again.
func TestCLIHandlerEvictSessionCutoffRace(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	gs1, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	// Record a cutoff time before re-accessing the session.
	cutoff := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Re-access the session, updating lastAccess to after the cutoff.
	gs1Again, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if gs1Again != gs1 {
		t.Fatal("expected same session on second access")
	}

	// EvictSession with the old cutoff should NOT evict, because the
	// session was accessed after the cutoff.
	h.EvictSession(ctx, root, cutoff)

	gs2, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if gs2 != gs1 {
		t.Error("session was evicted despite being accessed after the cutoff")
	}
}

func TestFindProjectRoot(t *testing.T) {
	// Test with go.mod present.
	root := testFiles(t)
	mainFile := filepath.Join(root, "main.go")
	got, err := goplscli.FindProjectRoot(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("FindProjectRoot(%q) = %q, want %q", mainFile, got, root)
	}

	// Test without go.mod — should return file's directory.
	noModDir := t.TempDir()
	noModDir, _ = filepath.EvalSymlinks(noModDir)
	goFile := filepath.Join(noModDir, "hello.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)

	got, err = goplscli.FindProjectRoot(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != noModDir {
		t.Errorf("FindProjectRoot(%q) = %q, want %q", goFile, got, noModDir)
	}
}

func TestCLIHandlerRoots(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	t.Cleanup(func() { h.Close(ctx) })

	if roots := h.Roots(); len(roots) != 0 {
		t.Errorf("expected 0 roots initially, got %d", len(roots))
	}

	_, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	roots := h.Roots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(roots))
	}
	if roots[0] != root {
		t.Errorf("root = %q, want %q", roots[0], root)
	}
}

func TestIdleTimeout(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	h := goplscli.NewCLIHandler(c)
	h.IdleTimeout = 100 * time.Millisecond
	h.EvictInterval = 50 * time.Millisecond
	t.Cleanup(func() { h.Close(ctx) })

	gs1, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatalf("SessionFor: %v", err)
	}

	// Wait long enough for the session to become idle and be evicted.
	time.Sleep(200 * time.Millisecond)

	gs2, err := h.SessionFor(ctx, root)
	if err != nil {
		t.Fatalf("SessionFor after eviction: %v", err)
	}
	if gs2 == gs1 {
		t.Error("expected a new session after idle eviction, got the same one")
	}
}

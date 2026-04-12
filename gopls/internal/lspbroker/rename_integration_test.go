// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/goadapter"
	"golang.org/x/tools/internal/jsonrpc2"
)

// TestBroker_RenameEndToEnd exercises the full rename stack:
//
//	broker.Serve ─▶ broker.handleRename ─▶ goadapter.GoSession
//	             ─▶ lspclient.Client ─▶ gopls subprocess ─▶ WorkspaceEdit
//	             ─▶ ApplyWorkspaceEdit ─▶ disk
//
// The test starts an in-process broker with goadapter.NewGoSession as
// its SessionFactory, performs the broker.handshake, and sends a
// lsp.rename request for the "Greeting" symbol in the testdata/foo
// fixture. It asserts that the files on disk are updated.
//
// The test is skipped when gopls is not on $PATH.
func TestBroker_RenameEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Copy the fixture project to a temp directory. If left under
	// testdata/ gopls will skip it as a build-ignored directory.
	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	libGo := filepath.Join(tmpDir, "lib.go")
	mainGo := filepath.Join(tmpDir, "main.go")

	cacheDir, err := os.MkdirTemp("", "lsp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(cacheDir) })

	l, err := lspbroker.NewListener(cacheDir)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	goplsPath, _ := os.Executable()
	b := lspbroker.NewBroker(goplsPath, "test")
	b.GoSessionFactory = func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		b.Serve(serveCtx, l)
	}()
	t.Cleanup(func() {
		cancelServe()
		b.Stop(context.Background())
		<-serveDone
	})

	nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// Send the rename request — Form B (positional): rename "Greeting" → "Hello".
	// lib.go line 5 col 6 (1-based) is the 'G' in "func Greeting".
	// Form B is used here rather than Form A (name-based) to avoid a race:
	// documentSymbol (used by Form A's resolveNamedPosition) can return
	// before gopls finishes type-checking, causing the subsequent rename to
	// fail with "no identifier found". The positional form skips the
	// documentSymbol pre-flight. Form A is tested separately by
	// TestBroker_DefinitionNameBased.
	renameParams := lspbroker.RenameParams{
		Version:   lspbroker.ProtocolVersion,
		File:      libGo,
		Line:      5,
		Character: lspbroker.IntPtr(6),
		NewName:   "Hello",
		DryRun:    false,
	}

	var result lspbroker.RenameResult
	if _, err := conn.Call(ctx, lspbroker.RenameMethod, renameParams, &result); err != nil {
		t.Fatalf("lsp.rename: %v", err)
	}

	if !result.Applied {
		t.Errorf("rename: Applied should be true")
	}
	if len(result.Changes) == 0 {
		t.Fatalf("rename: expected at least one changed file, got none")
	}
	t.Logf("rename applied %d file change(s):", len(result.Changes))
	for _, fc := range result.Changes {
		t.Logf("  %s (%d edits)", fc.Path, fc.Edits)
	}

	// lib.go must not contain "Greeting" and must contain "Hello".
	libContent, err := os.ReadFile(libGo)
	if err != nil {
		t.Fatalf("read lib.go: %v", err)
	}
	if strings.Contains(string(libContent), "Greeting") {
		t.Errorf("lib.go still contains 'Greeting' after rename:\n%s", libContent)
	}
	if !strings.Contains(string(libContent), "Hello") {
		t.Errorf("lib.go does not contain 'Hello' after rename:\n%s", libContent)
	}

	// main.go must also have the call site renamed. Note: the file's
	// documentation comment contains "Greeting" as a word, so we check
	// only the call expression form rather than the whole file.
	mainContent, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if strings.Contains(string(mainContent), "Greeting(") {
		t.Errorf("main.go still contains 'Greeting(' call after rename:\n%s", mainContent)
	}
	if !strings.Contains(string(mainContent), "Hello(") {
		t.Errorf("main.go does not contain 'Hello(' call after rename:\n%s", mainContent)
	}
}

// TestBroker_RenameDryRun verifies that --dry-run computes the edit plan
// without modifying any files on disk.
func TestBroker_RenameDryRun(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	libGo := filepath.Join(tmpDir, "lib.go")

	// Capture original file content to verify it is unchanged after dry-run.
	origLibContent, err := os.ReadFile(libGo)
	if err != nil {
		t.Fatalf("read lib.go: %v", err)
	}

	cacheDir, err := os.MkdirTemp("", "lsp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(cacheDir) })

	l, err := lspbroker.NewListener(cacheDir)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	goplsPath, _ := os.Executable()
	b := lspbroker.NewBroker(goplsPath, "test")
	b.GoSessionFactory = func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		b.Serve(serveCtx, l)
	}()
	t.Cleanup(func() {
		cancelServe()
		b.Stop(context.Background())
		<-serveDone
	})

	nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// Form B (positional): lib.go line 5 col 6 (1-based) is the 'G' in "func Greeting".
	renameParams := lspbroker.RenameParams{
		Version:   lspbroker.ProtocolVersion,
		File:      libGo,
		Line:      5,
		Character: lspbroker.IntPtr(6),
		NewName:   "Hello",
		DryRun:    true,
	}

	var result lspbroker.RenameResult
	if _, err := conn.Call(ctx, lspbroker.RenameMethod, renameParams, &result); err != nil {
		t.Fatalf("lsp.rename (dry-run): %v", err)
	}

	if result.Applied {
		t.Errorf("dry-run: Applied should be false")
	}
	if len(result.Changes) == 0 {
		t.Errorf("dry-run: expected non-empty changes list (edits plan), got none")
	}
	t.Logf("dry-run reports %d file change(s):", len(result.Changes))
	for _, fc := range result.Changes {
		t.Logf("  %s (%d edits)", fc.Path, fc.Edits)
	}

	// Verify files on disk are unchanged.
	afterLibContent, err := os.ReadFile(libGo)
	if err != nil {
		t.Fatalf("read lib.go after dry-run: %v", err)
	}
	if string(afterLibContent) != string(origLibContent) {
		t.Errorf("dry-run modified lib.go on disk (should not have):\nbefore: %q\nafter:  %q",
			origLibContent, afterLibContent)
	}
}

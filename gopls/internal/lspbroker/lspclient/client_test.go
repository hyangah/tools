// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker/lspclient"
	"golang.org/x/tools/gopls/internal/protocol"
)

// dialFakeClient creates a Client connected to a fakeServer via an in-process
// net.Pipe, completing the LSP initialize handshake. The test fails if
// initialization fails. The client and server are cleaned up via t.Cleanup.
func dialFakeClient(ctx context.Context, t *testing.T, fs *fakeServer, cfg lspclient.Config) *lspclient.Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})
	// Run the fake server in a background goroutine.
	go fs.serveConn(ctx, serverConn)

	c, err := lspclient.DialConn(ctx, cfg, clientConn)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Shutdown(shutCtx) //nolint:errcheck
	})
	return c
}

// ping flushes the server's notification queue by sending a round-trip call.
func ping(ctx context.Context, t *testing.T, c *lspclient.Client) {
	t.Helper()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestInitialize(t *testing.T) {
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}

	c := dialFakeClient(ctx, t, fs, cfg)

	if got := c.State(); got != lspclient.StateRunning {
		t.Errorf("State() = %v, want StateRunning", got)
	}
	if !fs.isInitialized() {
		t.Error("fake server did not receive initialize")
	}
}

func TestDidOpen(t *testing.T) {
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	uri := "file:///tmp/foo.go"
	content := []byte("package main\n")

	if err := c.DidOpen(ctx, uri, "go", content); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	ping(ctx, t, c)

	got := fs.openFileContent(uri)
	if got != string(content) {
		t.Errorf("server has %q, want %q", got, string(content))
	}
}

func TestDidOpen_Idempotent(t *testing.T) {
	// Second DidOpen on the same URI should send DidChange, not a second DidOpen.
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	uri := "file:///tmp/bar.go"
	first := []byte("package main\n")
	second := []byte("package main\nfunc main() {}\n")

	if err := c.DidOpen(ctx, uri, "go", first); err != nil {
		t.Fatalf("first DidOpen: %v", err)
	}
	if err := c.DidOpen(ctx, uri, "go", second); err != nil {
		t.Fatalf("second DidOpen: %v", err)
	}
	ping(ctx, t, c)

	got := fs.openFileContent(uri)
	if got != string(second) {
		t.Errorf("server has %q, want %q", got, string(second))
	}
}

func TestDidClose(t *testing.T) {
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	uri := "file:///tmp/close.go"
	if err := c.DidOpen(ctx, uri, "go", []byte("package main\n")); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	if err := c.DidClose(ctx, uri); err != nil {
		t.Fatalf("DidClose: %v", err)
	}
	ping(ctx, t, c)

	if got := fs.openFileContent(uri); got != "" {
		t.Errorf("server still has content for closed file: %q", got)
	}
}

func TestDefinition(t *testing.T) {
	ctx := context.Background()
	fs := newFakeServer(t)

	wantLoc := protocol.Location{
		URI: "file:///tmp/def.go",
		Range: protocol.Range{
			Start: protocol.Position{Line: 5, Character: 10},
			End:   protocol.Position{Line: 5, Character: 14},
		},
	}
	fs.onDefinition = func(params *protocol.DefinitionParams) ([]protocol.Location, error) {
		return []protocol.Location{wantLoc}, nil
	}

	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	locs, err := c.Definition(ctx, "file:///tmp/main.go", 10, 5)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Definition returned %d locations, want 1", len(locs))
	}
	if locs[0] != wantLoc {
		t.Errorf("Definition = %+v, want %+v", locs[0], wantLoc)
	}
}

func TestEnsureOpen(t *testing.T) {
	// Create a real temporary file and use EnsureOpen to sync it.
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	content := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	uri := string(protocol.URIFromPath(filePath))

	if err := c.EnsureOpen(ctx, filePath, "go"); err != nil {
		t.Fatalf("EnsureOpen: %v", err)
	}
	ping(ctx, t, c)

	if got := fs.openFileContent(uri); got != string(content) {
		t.Errorf("server has %q, want %q", got, string(content))
	}

	// Call EnsureOpen again without changing the file: should be a no-op.
	if err := c.EnsureOpen(ctx, filePath, "go"); err != nil {
		t.Fatalf("second EnsureOpen: %v", err)
	}
	ping(ctx, t, c)

	if got := fs.openFileContent(uri); got != string(content) {
		t.Errorf("after no-op EnsureOpen, server has %q, want %q", got, string(content))
	}

	// Update the file content and call EnsureOpen: should send DidChange.
	updated := []byte("package main\n\nfunc main() { println(\"hi\") }\n")
	if err := os.WriteFile(filePath, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	// Touch the file to change mtime (writes on the same second may have same mtime).
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(filePath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureOpen(ctx, filePath, "go"); err != nil {
		t.Fatalf("EnsureOpen after update: %v", err)
	}
	ping(ctx, t, c)

	if got := fs.openFileContent(uri); got != string(updated) {
		t.Errorf("after update, server has %q, want %q", got, string(updated))
	}
}

func TestOnDiagnostics(t *testing.T) {
	// Verify OnDiagnostics setter works without panicking.
	ctx := context.Background()
	fs := newFakeServer(t)
	cfg := lspclient.Config{RootURI: "file:///tmp"}
	c := dialFakeClient(ctx, t, fs, cfg)

	c.OnDiagnostics(func(uri string, version int32, diags []protocol.Diagnostic) {})
	// Replace with nil (should not panic).
	c.OnDiagnostics(nil)
}

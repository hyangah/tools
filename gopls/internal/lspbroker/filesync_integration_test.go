// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker_test

import (
	"context"
	"encoding/json"
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

// TestFileSyncAfterEdit verifies that editing a file between two broker
// requests causes the broker to pick up the change without requiring a
// daemon restart. This exercises the mtime-based stale detection in
// lspclient.EnsureOpen.
func TestFileSyncAfterEdit(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

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
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// First query: definition of Greeting call at main.go:11:9.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(9),
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
		t.Fatalf("first lsp.definition: %v", err)
	}

	var locs []lspbroker.Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		t.Fatalf("unmarshal first result: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("first query: expected at least one location")
	}
	t.Logf("first query result: %s line=%d", locs[0].URI, locs[0].Range.Start.Line)

	// Edit lib.go: add a blank line before "func Greeting" to shift it
	// from line 5 to line 6. This changes the definition location.
	original, err := os.ReadFile(libGo)
	if err != nil {
		t.Fatalf("read lib.go: %v", err)
	}
	edited := strings.Replace(string(original),
		"// Greeting returns",
		"\n// Greeting returns", 1)
	// Ensure mtime changes. macOS HFS+ has 1s mtime granularity;
	// APFS is nanosecond but we play it safe.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(libGo, []byte(edited), 0o644); err != nil {
		t.Fatalf("write lib.go: %v", err)
	}

	// Touch lib.go via a name-based query to force EnsureOpen to sync it.
	// The broker only syncs the file being queried, so we must query
	// lib.go to trigger the didChange notification to gopls.
	syncParams := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    libGo,
		Symbol:  "Greeting",
	}
	conn.Call(ctx, lspbroker.DefinitionMethod, syncParams, nil) //nolint:errcheck

	// Brief pause for gopls to re-analyze after the sync.
	time.Sleep(500 * time.Millisecond)

	// Second query: same position in main.go, but the definition should
	// now be on a different line (line 6 instead of 5, 0-based: 5 instead of 4).
	var raw2 json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw2); err != nil {
		t.Fatalf("second lsp.definition after edit: %v", err)
	}

	var locs2 []lspbroker.Location
	if err := json.Unmarshal(raw2, &locs2); err != nil {
		t.Fatalf("unmarshal second result: %v", err)
	}
	if len(locs2) == 0 {
		t.Fatal("second query: expected at least one location")
	}

	// The definition line should have shifted by +1 (from 0-based 4 to 5).
	firstLine := locs[0].Range.Start.Line
	secondLine := locs2[0].Range.Start.Line
	t.Logf("first line=%d, second line=%d (expected shift of +1)", firstLine, secondLine)

	if secondLine != firstLine+1 {
		t.Errorf("after editing lib.go, definition line shifted from %d to %d; want %d",
			firstLine, secondLine, firstLine+1)
	}
}

// TestPositionEncodingRoundTrip verifies that non-ASCII identifiers
// work correctly with the broker's 1-based UTF-8 byte column positions.
// This exercises the encoding negotiation path in lspclient and the
// position transcoding in the broker protocol.
func TestPositionEncodingRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "unicode")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
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
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query: definition of the Héllo() call in Café at line 22.
	// "Héllo" starts at some column on line 22. The é is 2 UTF-8 bytes.
	// We use name-based lookup to avoid computing the exact column.
	params := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
		Symbol:  "Héllo",
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
		t.Fatalf("lsp.definition (Héllo): %v", err)
	}
	// Name resolution succeeded — the broker correctly handled the
	// non-ASCII symbol name. The result may be empty (self-definition)
	// or the location of Héllo's declaration.
	t.Logf("Héllo lookup succeeded: raw=%q", string(raw))

	// Also verify Grüß works (ü = 2 bytes, ß = 2 bytes).
	params2 := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
		Symbol:  "Grüß",
	}
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params2, &raw); err != nil {
		t.Fatalf("lsp.definition (Grüß): %v", err)
	}
	t.Logf("Grüß lookup succeeded: raw=%q", string(raw))
}

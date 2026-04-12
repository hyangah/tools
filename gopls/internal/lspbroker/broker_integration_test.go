// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker_test

import (
	"context"
	"encoding/json"
	"io"
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

// TestBroker_DefinitionEndToEnd is the WS-G Phase 1 integration test.
// It exercises the full stack:
//
//	broker.Serve ─▶ broker.handleDefinition ─▶ goadapter.GoSession
//	             ─▶ lspclient.Client ─▶ gopls subprocess ─▶ result
//
// The test starts an in-process broker with goadapter.NewGoSession as
// its SessionFactory (not the stub), connects a jsonrpc2 client,
// performs the required broker.handshake, and sends a single
// lsp.definition request for a known call site in the testdata/foo
// fixture. It asserts the returned Location points at lib.go at the
// LSP 0-based position that corresponds to "func Greeting" (1-based
// line 5 col 6).
//
// This is the test that closes Phase 1 of the lsp-broker project —
// it is the first thing that exercises the full broker+adapter+gopls
// path end-to-end on an actual Go source file. See
// ~/proj/kb-lsp-skills/IMPLEMENTATION_PLAN.md Phase 1 verification.
//
// The test is skipped when gopls is not on $PATH, per the project
// integration-test convention (AGENTS.md §Tests).
func TestBroker_DefinitionEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Copy the fixture project to a temp directory. If left under
	// testdata/ gopls will skip it as a build-ignored directory, which
	// makes textDocument/definition return no results.
	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")

	// Stand up a broker listening on an in-process unix socket, using a
	// real SessionFactory that spawns gopls. The cache dir uses
	// os.MkdirTemp with a 3-char prefix rather than t.TempDir() because
	// t.TempDir() paths on macOS embed the test name and the numeric
	// subdir counter, which easily pushes the final broker.sock path
	// past the 104-byte sockaddr_un.sun_path limit. There is a latent
	// off-by-one in lspbroker.NewListener's maxUnixSockPath guard
	// (it uses "> 104" where it should use ">= 104" — a 104-char path
	// fails to bind because the kernel also needs a NUL terminator);
	// see OPEN_QUESTIONS.md for the follow-up. The workaround here is
	// purely in the test and does not touch shipped code.
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

	factory := func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}
	goplsPath, _ := os.Executable()
	b := lspbroker.NewBroker(goplsPath, "test", factory)

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()
	// Cleanup order (LIFO — registered last, runs first): close the
	// jsonrpc2 conn, close the underlying socket, then stop the broker
	// (which closes the session and waits for gopls to exit), then wait
	// for the Serve goroutine to return.
	t.Cleanup(func() {
		cancelServe()
		_ = b.Stop(context.Background())
		<-serveDone
	})

	// Dial the broker. Use Addr() rather than reconstructing the path —
	// NewListener may have fallen back to $TMPDIR when cacheDir was too
	// long for macOS's 104-byte unix socket limit.
	nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	// Use a generous per-operation deadline: gopls startup + module
	// load on a cold cache can take several seconds even for a
	// single-file fixture.
	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	// Handshake is mandatory before any lsp.* method (ADR-005).
	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query: main.go:11:9 — the call site of Greeting. See the comment
	// in testdata/foo/main.go for the exact position.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: 9,
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
		t.Fatalf("lsp.definition: %v", err)
	}

	var locs []lspbroker.Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("expected at least one definition location, got none")
	}

	got := locs[0]
	t.Logf("got %d location(s); first: uri=%s line=%d character=%d",
		len(locs), got.URI, got.Range.Start.Line, got.Range.Start.Character)

	// Verify the result points at lib.go (where Greeting is defined).
	gotPath := strings.TrimPrefix(got.URI, "file://")
	if !strings.HasSuffix(gotPath, "lib.go") {
		t.Errorf("definition URI path = %q, want a path ending in lib.go", gotPath)
	}

	// lib.go line 5 col 6 (1-based, "func Greeting" — the identifier
	// starts at column 6, after "func ") corresponds to LSP 0-based
	// line 4 char 5. The broker returns raw LSP positions per ADR-006 /
	// designs/02-broker-protocol.md — the 1-based reformatting happens
	// in the CLI's format layer, not the broker wire.
	if got.Range.Start.Line != 4 {
		t.Errorf("definition result start line = %d (0-based), want 4 (= lib.go line 5)",
			got.Range.Start.Line)
	}
	if got.Range.Start.Character != 5 {
		t.Errorf("definition result start character = %d (0-based), want 5 (= lib.go col 6 after 'func ')",
			got.Range.Start.Character)
	}
}

// copyDir recursively copies the contents of src into dst (which must
// exist). Duplicated from goadapter/adapter_integration_test.go because
// that file lives in a different external test package and its helpers
// are unexported; the copy is small and has no project-specific logic.
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

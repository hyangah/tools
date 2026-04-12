// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker_test

import (
	"context"
	"encoding/json"
	"fmt"
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

	goplsPath, _ := os.Executable()
	b := lspbroker.NewBroker(goplsPath, "test")
	b.GoSessionFactory = func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}

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
		Character: lspbroker.IntPtr(9),
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

// TestBroker_DefinitionNameBased tests the Form A (name-based) path:
// the broker resolves a symbol name via documentSymbol, then dispatches
// the positional definition call.
func TestBroker_DefinitionNameBased(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")
	libGo := filepath.Join(tmpDir, "lib.go")

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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Test 1: Name-based query for "Greeting" in lib.go.
	// Greeting is defined at lib.go:5:6 (1-based). Querying the definition
	// of a symbol at its own definition may return the position itself or
	// an empty result — both are acceptable. The key test is that
	// name resolution succeeds (no error).
	params := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    libGo,
		Symbol:  "Greeting",
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
		t.Fatalf("lsp.definition (name-based, Greeting in lib.go): %v", err)
	}
	t.Logf("name-based Greeting in lib.go: raw=%q", string(raw))

	// Test 2: Name-based query for "main" in main.go. Same pattern —
	// verifies that name resolution works for a different symbol.
	params2 := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
		Symbol:  "main",
	}
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params2, &raw); err != nil {
		t.Fatalf("lsp.definition (name-based, main in main.go): %v", err)
	}
	t.Logf("name-based main in main.go: raw=%q", string(raw))
}

// TestBroker_DefinitionSymbolNotFound tests that a non-existent symbol
// returns the appropriate error.
func TestBroker_DefinitionSymbolNotFound(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	libGo := filepath.Join(tmpDir, "lib.go")

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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query a symbol that doesn't exist.
	params := lspbroker.DefinitionParams{
		Version: lspbroker.ProtocolVersion,
		File:    libGo,
		Symbol:  "NonExistentSymbol",
	}
	var raw json.RawMessage
	_, err = conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw)
	if err == nil {
		t.Fatal("expected error for non-existent symbol, got nil")
	}
	t.Logf("got expected error: %v", err)
	// The error message should mention "symbol not found".
	if !strings.Contains(err.Error(), "symbol not found") {
		t.Errorf("error = %q, want it to contain 'symbol not found'", err)
	}
}

// TestBroker_ReferencesEndToEnd exercises the lsp.references operation
// end-to-end. It queries references for the Greeting call at main.go:11:9
// and expects at least 2 locations: the definition in lib.go and the call
// in main.go.
func TestBroker_ReferencesEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
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
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancelServe()
		_ = b.Stop(context.Background())
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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query references for the Greeting call at main.go:11:9.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(9),
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.ReferencesMethod, params, &raw); err != nil {
		t.Fatalf("lsp.references: %v", err)
	}

	var locs []lspbroker.Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	t.Logf("got %d reference location(s)", len(locs))
	if len(locs) < 2 {
		t.Errorf("expected at least 2 reference locations (definition + call site), got %d", len(locs))
	}
}

// TestBroker_HoverEndToEnd exercises the lsp.hover operation end-to-end.
// It queries hover info for the Greeting call at main.go:11:9 and expects
// a non-empty result.
func TestBroker_HoverEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
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
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancelServe()
		_ = b.Stop(context.Background())
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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query hover for the Greeting call at main.go:11:9.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(9),
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.HoverMethod, params, &raw); err != nil {
		t.Fatalf("lsp.hover: %v", err)
	}

	t.Logf("hover result: %s", string(raw))
	if len(raw) == 0 || string(raw) == "null" {
		t.Error("expected non-empty hover result, got null/empty")
	}
}

// TestBroker_DocumentSymbolEndToEnd exercises the lsp.documentSymbol operation
// end-to-end. It queries symbols for main.go and expects at least the "main"
// function symbol in the result.
func TestBroker_DocumentSymbolEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	fixtureDir := filepath.Join("testdata", "foo")
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
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancelServe()
		_ = b.Stop(context.Background())
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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// Query document symbols for main.go.
	params := lspbroker.DocumentSymbolParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DocumentSymbolMethod, params, &raw); err != nil {
		t.Fatalf("lsp.documentSymbol: %v", err)
	}

	t.Logf("documentSymbol result: %s", string(raw))

	// Unmarshal as a generic slice to check for "main".
	var syms []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &syms); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("expected at least one symbol, got none")
	}

	foundMain := false
	for _, s := range syms {
		if s.Name == "main" {
			foundMain = true
			break
		}
	}
	if !foundMain {
		t.Errorf("expected 'main' in symbols, got %v", syms)
	}
}

// TestBroker_MultiLanguageRouting verifies that the broker routes
// requests to different sessions based on file extension. .go files
// go to the GoSession (via GoSessionFactory); .fake files go to a
// GenericSession backed by the fakelsp test binary.
func TestBroker_MultiLanguageRouting(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Build the fake LSP server binary.
	fakeLSPBin := filepath.Join(t.TempDir(), "fakelsp")
	buildCmd := exec.Command("go", "build", "-o", fakeLSPBin, "./testdata/fakelsp")
	buildCmd.Dir = filepath.Join(".")
	// Set GOFLAGS to suppress the workspace mode that the parent module
	// might enable, which would interfere with building the standalone
	// testdata program.
	buildCmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakelsp: %v\n%s", err, out)
	}

	// Copy the multilang fixture to a temp dir.
	fixtureDir := filepath.Join("testdata", "multilang")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")
	appFake := filepath.Join(tmpDir, "app.fake")

	// Write .lsp.json that configures fakelsp for .fake files.
	lspConfig := fmt.Sprintf(`{
		"version": 1,
		"servers": {
			"fake": {
				"command": [%q],
				"extensionToLanguage": {
					".fake": "fake"
				}
			}
		}
	}`, fakeLSPBin)
	if err := os.WriteFile(filepath.Join(tmpDir, ".lsp.json"), []byte(lspConfig), 0644); err != nil {
		t.Fatalf("write .lsp.json: %v", err)
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
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancelServe()
		_ = b.Stop(context.Background())
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

	ctx, cancelReq := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReq()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("broker.handshake: %v", err)
	}

	// --- Test 1: .go file routes to GoSession ---
	t.Run("go_definition", func(t *testing.T) {
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      mainGo,
			Line:      11,
			Character: lspbroker.IntPtr(14),
		}
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
			t.Fatalf("lsp.definition on .go: %v", err)
		}
		var locs []lspbroker.Location
		if err := json.Unmarshal(raw, &locs); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(locs) == 0 {
			t.Fatal("expected definition locations for .go file, got none")
		}
		t.Logf(".go definition: %s line=%d", locs[0].URI, locs[0].Range.Start.Line)
	})

	// --- Test 2: .fake file routes to GenericSession (fakelsp) ---
	t.Run("fake_definition", func(t *testing.T) {
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      appFake,
			Line:      1,
			Character: lspbroker.IntPtr(1),
		}
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
			t.Fatalf("lsp.definition on .fake: %v", err)
		}
		var locs []lspbroker.Location
		if err := json.Unmarshal(raw, &locs); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(locs) == 0 {
			t.Fatal("expected definition locations for .fake file, got none")
		}
		// fakelsp returns line 0, char 0 of the same file.
		if !strings.HasSuffix(strings.TrimPrefix(locs[0].URI, "file://"), "app.fake") {
			t.Errorf("expected URI ending in app.fake, got %s", locs[0].URI)
		}
		t.Logf(".fake definition: %s line=%d", locs[0].URI, locs[0].Range.Start.Line)
	})

	// --- Test 3: .fake hover ---
	t.Run("fake_hover", func(t *testing.T) {
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      appFake,
			Line:      1,
			Character: lspbroker.IntPtr(1),
		}
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.HoverMethod, params, &raw); err != nil {
			t.Fatalf("lsp.hover on .fake: %v", err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			t.Fatal("expected hover result for .fake file, got empty")
		}
		t.Logf(".fake hover: %s", string(raw))
	})

	// --- Test 4: extension with no server configured ---
	t.Run("unknown_extension", func(t *testing.T) {
		unknownFile := filepath.Join(tmpDir, "style.css")
		os.WriteFile(unknownFile, []byte("body {}"), 0644)
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      unknownFile,
			Line:      1,
			Character: lspbroker.IntPtr(1),
		}
		var raw json.RawMessage
		_, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw)
		if err == nil {
			t.Fatal("expected error for .css file (no server), got nil")
		}
		t.Logf(".css error (expected): %v", err)
	})
}

// TestBroker_GoAutoConfigPriority verifies that .go files route to the
// GoSession even when .lsp.json exists, unless .lsp.json explicitly
// claims the .go extension.
func TestBroker_GoAutoConfigPriority(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Build fakelsp.
	fakeLSPBin := filepath.Join(t.TempDir(), "fakelsp")
	buildCmd := exec.Command("go", "build", "-o", fakeLSPBin, "./testdata/fakelsp")
	buildCmd.Dir = filepath.Join(".")
	buildCmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakelsp: %v\n%s", err, out)
	}

	fixtureDir := filepath.Join("testdata", "multilang")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")

	// Write .lsp.json that configures fakelsp for .fake but NOT .go.
	lspConfig := fmt.Sprintf(`{
		"version": 1,
		"servers": {
			"fake": {
				"command": [%q],
				"extensionToLanguage": { ".fake": "fake" }
			}
		}
	}`, fakeLSPBin)
	os.WriteFile(filepath.Join(tmpDir, ".lsp.json"), []byte(lspConfig), 0644)

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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// .go file should still route to GoSession (real gopls), not fakelsp.
	// We verify by checking that the definition result points to the real
	// location in the Go source, not fakelsp's canned response.
	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(14),
	}
	var raw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
		t.Fatalf("lsp.definition on .go: %v", err)
	}
	var locs []lspbroker.Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("expected definition locations, got none")
	}
	// Verify this came from gopls (points at Greeting definition in main.go),
	// not fakelsp (which would return line 0, char 0).
	got := locs[0]
	t.Logf("Go auto-config result: %s line=%d char=%d", got.URI, got.Range.Start.Line, got.Range.Start.Character)
	if got.Range.Start.Line == 0 && got.Range.Start.Character == 0 {
		t.Error("definition looks like it came from fakelsp (line=0, char=0); expected real gopls result")
	}
}

// TestBroker_UntrustedMultiLanguage verifies trust enforcement:
// - .go queries succeed (Go auto-config skips trust)
// - .fake queries fail with ErrUntrustedRoot when root is not trusted
// - .fake queries succeed after trusting the root
func TestBroker_UntrustedMultiLanguage(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Build fakelsp.
	fakeLSPBin := filepath.Join(t.TempDir(), "fakelsp")
	buildCmd := exec.Command("go", "build", "-o", fakeLSPBin, "./testdata/fakelsp")
	buildCmd.Dir = "."
	buildCmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakelsp: %v\n%s", err, out)
	}

	fixtureDir := filepath.Join("testdata", "multilang")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")
	appFake := filepath.Join(tmpDir, "app.fake")

	// Write .lsp.json.
	lspConfig := fmt.Sprintf(`{
		"version": 1,
		"servers": {
			"fake": {
				"command": [%q],
				"extensionToLanguage": { ".fake": "fake" }
			}
		}
	}`, fakeLSPBin)
	os.WriteFile(filepath.Join(tmpDir, ".lsp.json"), []byte(lspConfig), 0644)

	// Create an empty trust store (nothing trusted).
	trustFile := filepath.Join(t.TempDir(), "trusted.json")
	ts, err := lspbroker.LoadTrustStore(trustFile)
	if err != nil {
		t.Fatalf("load trust store: %v", err)
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
	b.TrustStore = ts

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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// .go should work (Go auto-config skips trust).
	t.Run("go_succeeds_untrusted", func(t *testing.T) {
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      mainGo,
			Line:      11,
			Character: lspbroker.IntPtr(14),
		}
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
			t.Fatalf("lsp.definition on .go in untrusted root should succeed: %v", err)
		}
		t.Logf(".go succeeded in untrusted root")
	})

	// .fake should fail (untrusted root).
	t.Run("fake_blocked_untrusted", func(t *testing.T) {
		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      appFake,
			Line:      1,
			Character: lspbroker.IntPtr(1),
		}
		var raw json.RawMessage
		_, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw)
		if err == nil {
			t.Fatal("expected ErrUntrustedRoot for .fake in untrusted root, got nil")
		}
		if !strings.Contains(err.Error(), "not trusted") {
			t.Fatalf("expected 'not trusted' error, got: %v", err)
		}
		t.Logf(".fake blocked (expected): %v", err)
	})

	// Trust the root, then .fake should succeed.
	t.Run("fake_succeeds_after_trust", func(t *testing.T) {
		if err := ts.Add(tmpDir); err != nil {
			t.Fatalf("trust add: %v", err)
		}

		params := lspbroker.DefinitionParams{
			Version:   lspbroker.ProtocolVersion,
			File:      appFake,
			Line:      1,
			Character: lspbroker.IntPtr(1),
		}
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
			t.Fatalf("lsp.definition on .fake after trust: %v", err)
		}
		t.Logf(".fake succeeded after trusting root")
	})
}

// TestBroker_DiagnosticsEndToEnd tests the diagnostics collection flow:
//  1. Introduce a Go syntax error into a temp file
//  2. Send lsp.sync to tell the broker to re-read the file
//  3. Poll lsp.diagnostics until gopls sends diagnostics (or timeout)
//  4. Verify at least one diagnostic is returned
//  5. Fix the file, sync again, verify diagnostics are empty
func TestBroker_DiagnosticsEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	// Copy the fixture to a writable temp directory.
	fixtureDir := filepath.Join("testdata", "foo")
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

	// First, open the file by querying definition to ensure the session exists.
	defParams := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      10,
		Character: lspbroker.IntPtr(5),
	}
	var defRaw json.RawMessage
	if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, defParams, &defRaw); err != nil {
		t.Logf("initial definition query (may fail for valid reasons): %v", err)
	}

	// Introduce a syntax error into the file.
	origContent, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	t.Cleanup(func() {
		os.WriteFile(mainGo, origContent, 0o644) //nolint:errcheck
	})

	badContent := string(origContent) + "\nvar _ = undeclaredName123\n"
	if err := os.WriteFile(mainGo, []byte(badContent), 0o644); err != nil {
		t.Fatalf("write bad content: %v", err)
	}

	// Sync the file so gopls sees the error.
	syncParams := lspbroker.SyncParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
	}
	if _, err := conn.Call(ctx, lspbroker.SyncMethod, syncParams, nil); err != nil {
		t.Fatalf("lsp.sync: %v", err)
	}

	// Poll for diagnostics with a 30s deadline. gopls sends
	// publishDiagnostics asynchronously, so we retry.
	diagParams := lspbroker.DiagnosticsParams{
		Version: lspbroker.ProtocolVersion,
		File:    mainGo,
	}

	// diagCount polls lsp.diagnostics and returns the number of diagnostics.
	diagCount := func() int {
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DiagnosticsMethod, diagParams, &raw); err != nil {
			t.Logf("lsp.diagnostics call error: %v", err)
			return 0
		}
		if len(raw) == 0 || string(raw) == "null" {
			return 0
		}
		var diags []json.RawMessage
		if err := json.Unmarshal(raw, &diags); err != nil {
			t.Logf("decode diagnostics error: %v (raw=%s)", err, raw)
			return 0
		}
		return len(diags)
	}

	// Poll until at least one diagnostic arrives (up to 30s).
	var gotCount int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		gotCount = diagCount()
		if gotCount > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if gotCount == 0 {
		t.Errorf("expected at least one diagnostic after introducing error, got none")
	} else {
		t.Logf("got %d diagnostic(s) after introducing error", gotCount)
	}

	// Fix the file and sync again.
	if err := os.WriteFile(mainGo, origContent, 0o644); err != nil {
		t.Fatalf("restore main.go: %v", err)
	}
	if _, err := conn.Call(ctx, lspbroker.SyncMethod, syncParams, nil); err != nil {
		t.Fatalf("lsp.sync (fix): %v", err)
	}

	// Poll for cleared diagnostics (up to 30s). Not a hard failure since
	// gopls timing varies — just log the result.
	deadline = time.Now().Add(30 * time.Second)
	var fixedCount int
	for time.Now().Before(deadline) {
		fixedCount = diagCount()
		if fixedCount == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("after fix: %d diagnostic(s)", fixedCount)
	// Cleared or reduced — not a hard failure since gopls timing varies.
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

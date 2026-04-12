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
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/goadapter"
	"golang.org/x/tools/internal/jsonrpc2"
)

// setupBrokerConn starts an in-process broker using goadapter.NewGoSession,
// copies the testdata/foo fixture to a temp dir, dials a fresh connection,
// performs the handshake, and returns the connection, the path to main.go,
// and a cancel func that tears everything down.
//
// The caller is responsible for calling the returned cancel func.
func setupBrokerConn(tb testing.TB) (conn jsonrpc2.Conn, mainGo string, cancel func()) {
	tb.Helper()

	goplsPath, err := exec.LookPath("gopls")
	if err != nil {
		tb.Skip("gopls not on PATH")
	}

	// Copy fixture to a temp dir. gopls ignores files under testdata/.
	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := tb.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		tb.Fatalf("copy fixture: %v", err)
	}
	mainGo = filepath.Join(tmpDir, "main.go")

	// Use os.MkdirTemp (not tb.TempDir) so the socket path stays short
	// enough for macOS's 104-byte sockaddr_un.sun_path limit.
	cacheDir, err := os.MkdirTemp("", "lsp")
	if err != nil {
		tb.Fatalf("MkdirTemp: %v", err)
	}

	l, err := lspbroker.NewListener(cacheDir)
	if err != nil {
		os.RemoveAll(cacheDir)
		tb.Fatalf("NewListener: %v", err)
	}

	b := lspbroker.NewBroker(goplsPath, "test")
	b.GoSessionFactory = func(root string) lspbroker.Session {
		return goadapter.NewGoSession(root)
	}

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if err := b.Serve(serveCtx, l); err != nil && serveCtx.Err() == nil {
			tb.Logf("broker.Serve: %v", err)
		}
	}()

	nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
	if err != nil {
		cancelServe()
		os.RemoveAll(cacheDir)
		tb.Fatalf("dial broker: %v", err)
	}

	stream := jsonrpc2.NewHeaderStream(nc)
	conn = jsonrpc2.NewConn(stream)
	conn.Go(serveCtx, jsonrpc2.MethodNotFound)

	ctx, cancelHandshake := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelHandshake()
	if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
		conn.Close()
		nc.Close()
		cancelServe()
		os.RemoveAll(cacheDir)
		tb.Fatalf("broker.handshake: %v", err)
	}

	cancel = func() {
		conn.Close()
		nc.Close()
		l.Close()
		cancelServe()
		_ = b.Stop(context.Background())
		<-serveDone
		os.RemoveAll(cacheDir)
	}
	return conn, mainGo, cancel
}

// BenchmarkDefinition measures steady-state latency of lsp.definition
// requests over a single persistent connection. The broker and gopls
// session are warmed up before b.ResetTimer() so the loop only measures
// the round-trip cost of the RPC itself.
func BenchmarkDefinition(b *testing.B) {
	if _, err := exec.LookPath("gopls"); err != nil {
		b.Skip("gopls not on PATH")
	}

	conn, mainGo, cancel := setupBrokerConn(b)
	defer cancel()

	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(9),
	}

	// Warm up: one throwaway call so gopls finishes loading the module
	// before we start timing.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer warmCancel()
	var warmRaw json.RawMessage
	if _, err := conn.Call(warmCtx, lspbroker.DefinitionMethod, params, &warmRaw); err != nil {
		b.Fatalf("warm-up lsp.definition: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); err != nil {
			cancel()
			b.Fatalf("lsp.definition iteration %d: %v", i, err)
		}
		cancel()
	}
}

// BenchmarkHover measures steady-state latency of lsp.hover requests
// over a single persistent connection. Same warm-up pattern as
// BenchmarkDefinition.
func BenchmarkHover(b *testing.B) {
	if _, err := exec.LookPath("gopls"); err != nil {
		b.Skip("gopls not on PATH")
	}

	conn, mainGo, cancel := setupBrokerConn(b)
	defer cancel()

	params := lspbroker.DefinitionParams{
		Version:   lspbroker.ProtocolVersion,
		File:      mainGo,
		Line:      11,
		Character: lspbroker.IntPtr(9),
	}

	// Warm up: one throwaway call so gopls finishes loading the module.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer warmCancel()
	var warmRaw json.RawMessage
	if _, err := conn.Call(warmCtx, lspbroker.HoverMethod, params, &warmRaw); err != nil {
		b.Fatalf("warm-up lsp.hover: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var raw json.RawMessage
		if _, err := conn.Call(ctx, lspbroker.HoverMethod, params, &raw); err != nil {
			cancel()
			b.Fatalf("lsp.hover iteration %d: %v", i, err)
		}
		cancel()
	}
}

// TestLoadParallelDefinition launches 10 goroutines, each opening its own
// connection to a shared in-process broker, and sends a lsp.definition
// request concurrently. All requests must succeed. Run with -race to
// exercise the broker's concurrency guards.
func TestLoadParallelDefinition(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping load test")
	}

	// Copy fixture to a temp dir. gopls ignores files under testdata/.
	fixtureDir := filepath.Join("testdata", "foo")
	tmpDir := t.TempDir()
	if err := copyDir(fixtureDir, tmpDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	mainGo := filepath.Join(tmpDir, "main.go")

	// Use os.MkdirTemp (not t.TempDir) so the socket path stays short
	// enough for macOS's 104-byte sockaddr_un.sun_path limit.
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

	goplsPath, _ := exec.LookPath("gopls")
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

	const numGoroutines = 10

	// Collect errors from goroutines via a buffered channel to avoid
	// t.Fatal from a non-test goroutine (which would panic).
	errs := make(chan error, numGoroutines)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		i := i // capture loop variable
		go func() {
			defer wg.Done()

			// Each goroutine opens its own connection to the broker.
			nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
			if err != nil {
				errs <- err
				return
			}
			defer nc.Close()

			stream := jsonrpc2.NewHeaderStream(nc)
			conn := jsonrpc2.NewConn(stream)
			conn.Go(serveCtx, jsonrpc2.MethodNotFound)
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			if _, err := lspbroker.Handshake(ctx, conn, goplsPath, "test"); err != nil {
				errs <- err
				return
			}

			params := lspbroker.DefinitionParams{
				Version:   lspbroker.ProtocolVersion,
				File:      mainGo,
				Line:      11,
				Character: lspbroker.IntPtr(9),
			}
			var raw json.RawMessage
			if _, callErr := conn.Call(ctx, lspbroker.DefinitionMethod, params, &raw); callErr != nil {
				errs <- callErr
				return
			}

			var locs []lspbroker.Location
			if unmarshalErr := json.Unmarshal(raw, &locs); unmarshalErr != nil {
				errs <- unmarshalErr
				return
			}
			if len(locs) == 0 {
				errs <- &emptyResultError{goroutine: i}
				return
			}
			t.Logf("goroutine %d: got %d location(s)", i, len(locs))
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("goroutine error: %v", err)
	}
}

// emptyResultError is returned when a lsp.definition call succeeds but
// returns no locations, so we can identify which goroutine failed.
type emptyResultError struct {
	goroutine int
}

func (e *emptyResultError) Error() string {
	return "lsp.definition returned no locations"
}

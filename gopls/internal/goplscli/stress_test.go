// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// TestConcurrentClients simulates 10 concurrent CLI connections to the same
// daemon, each making definition queries on different positions.
func TestConcurrentClients(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	// Sync the file first so all concurrent queries can proceed.
	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodSync,
		File:   mainFile,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("sync error: %s", resp.Error)
	}

	// Define queries at different positions in main.go.
	// Each targets a known identifier with an expected definition line.
	type query struct {
		line   int // 1-based
		col    int // 1-based UTF-8
		wantLn int // expected 1-based line of definition
		desc   string
	}
	queries := []query{
		{line: 11, col: 9, wantLn: 6, desc: "Greeting call"},       // msg := Greeting("world")
		{line: 11, col: 2, wantLn: 11, desc: "msg declaration"},    // msg := ...
		{line: 12, col: 6, wantLn: 0, desc: "Println (stdlib)"},    // fmt.Println — stdlib, just check no error
		{line: 6, col: 6, wantLn: 6, desc: "Greeting declaration"}, // func Greeting
		{line: 7, col: 14, wantLn: 0, desc: "Sprintf (stdlib)"},    // fmt.Sprintf — stdlib
	}

	const numClients = 10
	var wg sync.WaitGroup
	errs := make(chan string, numClients)

	for i := range numClients {
		wg.Add(1)
		q := queries[i%len(queries)]
		go func(clientID int, q query) {
			defer wg.Done()
			resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
				Method: goplscli.MethodDefinition,
				File:   mainFile,
				Line:   q.line,
				Column: q.col,
			})
			if err != nil {
				errs <- fmt.Sprintf("client %d (%s): request error: %v", clientID, q.desc, err)
				return
			}
			if resp.Error != "" {
				errs <- fmt.Sprintf("client %d (%s): response error: %s", clientID, q.desc, resp.Error)
				return
			}
			if len(resp.Locations) == 0 {
				errs <- fmt.Sprintf("client %d (%s): no locations returned", clientID, q.desc)
				return
			}
			if q.wantLn > 0 && resp.Locations[0].Start.Line != q.wantLn {
				errs <- fmt.Sprintf("client %d (%s): got line %d, want %d", clientID, q.desc, resp.Locations[0].Start.Line, q.wantLn)
			}
		}(i, q)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestConcurrentDifferentWorkspaces creates two separate test modules and
// sends concurrent requests targeting files in different workspaces, verifying
// that the CLIHandler creates separate sessions for each root.
func TestConcurrentDifferentWorkspaces(t *testing.T) {
	// Create two independent test modules.
	root1 := createModule(t, "mod1", map[string]string{
		"go.mod": "module example.com/mod1\n\ngo 1.21\n",
		"lib.go": `package mod1

// Add returns a + b.
func Add(a, b int) int {
	return a + b
}
`,
	})
	root2 := createModule(t, "mod2", map[string]string{
		"go.mod": "module example.com/mod2\n\ngo 1.21\n",
		"lib.go": `package mod2

// Mul returns a * b.
func Mul(a, b int) int {
	return a * b
}
`,
	})

	c := cache.New(nil)
	addr := startTestServerShort(t, c)
	ctx := t.Context()

	file1 := filepath.Join(root1, "lib.go")
	file2 := filepath.Join(root2, "lib.go")

	// Sync both files.
	for _, f := range []string{file1, file2} {
		resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
			Method: goplscli.MethodSync,
			File:   f,
		})
		if err != nil {
			t.Fatalf("sync %s: %v", f, err)
		}
		if resp.Error != "" {
			t.Fatalf("sync %s error: %s", f, resp.Error)
		}
	}

	// Send concurrent definition queries to both modules.
	const numPerModule = 5
	var wg sync.WaitGroup
	errs := make(chan string, numPerModule*2)

	for i := range numPerModule {
		// Module 1: definition of "Add" at line 4, col 6.
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
				Method: goplscli.MethodDefinition,
				File:   file1,
				Line:   4,
				Column: 6,
			})
			if err != nil {
				errs <- fmt.Sprintf("mod1 client %d: %v", id, err)
				return
			}
			if resp.Error != "" {
				errs <- fmt.Sprintf("mod1 client %d: %s", id, resp.Error)
				return
			}
			if len(resp.Locations) == 0 {
				errs <- fmt.Sprintf("mod1 client %d: no locations", id)
				return
			}
			loc := resp.Locations[0]
			if loc.Start.Line != 4 {
				errs <- fmt.Sprintf("mod1 client %d: got line %d, want 4", id, loc.Start.Line)
			}
			// Verify the result is in the correct module.
			if loc.File != file1 {
				errs <- fmt.Sprintf("mod1 client %d: got file %s, want %s", id, loc.File, file1)
			}
		}(i)

		// Module 2: definition of "Mul" at line 4, col 6.
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
				Method: goplscli.MethodDefinition,
				File:   file2,
				Line:   4,
				Column: 6,
			})
			if err != nil {
				errs <- fmt.Sprintf("mod2 client %d: %v", id, err)
				return
			}
			if resp.Error != "" {
				errs <- fmt.Sprintf("mod2 client %d: %s", id, resp.Error)
				return
			}
			if len(resp.Locations) == 0 {
				errs <- fmt.Sprintf("mod2 client %d: no locations", id)
				return
			}
			loc := resp.Locations[0]
			if loc.Start.Line != 4 {
				errs <- fmt.Sprintf("mod2 client %d: got line %d, want 4", id, loc.Start.Line)
			}
			if loc.File != file2 {
				errs <- fmt.Sprintf("mod2 client %d: got file %s, want %s", id, loc.File, file2)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestSnapshotLeakDetection creates a GoSession, runs several queries, then
// calls Close (which calls session.Shutdown) with a timeout. If Shutdown
// doesn't return within the timeout, the test fails, indicating a snapshot
// leak from missing defer release() calls.
func TestSnapshotLeakDetection(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)

	// Create the session manually (not via newTestSession) so we control shutdown.
	sess := cache.NewSession(ctx, c)
	opts := settings.DefaultOptions()
	folderURI := protocol.URIFromPath(root)
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		t.Fatalf("FetchGoEnv: %v", err)
	}
	folder := &cache.Folder{
		Dir:     folderURI,
		Name:    filepath.Base(root),
		Options: opts,
		Env:     *env,
	}
	_, _, release, err := sess.NewView(ctx, folder)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	release()

	gs := goplscli.NewGoSession(root, sess)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	// Run several different query types to exercise all code paths.
	uri := protocol.URIFromPath(mainFile)
	rng := protocol.Range{
		Start: protocol.Position{Line: 10, Character: 8},
		End:   protocol.Position{Line: 10, Character: 8},
	}

	if _, err := gs.Definition(ctx, uri, rng); err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if _, err := gs.References(ctx, uri, rng, true); err != nil {
		t.Fatalf("References: %v", err)
	}
	if _, err := gs.Hover(ctx, uri, rng); err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if _, err := gs.DocumentSymbols(ctx, uri); err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if _, err := gs.DiagnoseFile(ctx, uri); err != nil {
		t.Fatalf("DiagnoseFile: %v", err)
	}

	// Shutdown with a timeout. If snapshots leaked, Shutdown blocks forever.
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		gs.Close(shutdownCtx)
		close(done)
	}()

	select {
	case <-done:
		t.Log("Shutdown completed successfully -- no snapshot leaks detected")
	case <-shutdownCtx.Done():
		t.Fatal("Shutdown timed out after 5s -- possible snapshot leak (missing defer release())")
	}
}

// TestRapidFireQueries sends 50 sequential definition queries to the same
// file as fast as possible. This tests that the fingerprint cache in
// EnsureSynced works correctly (skipping re-reads after the first call).
func TestRapidFireQueries(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)
	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	// Sync once to prime the session.
	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodSync,
		File:   mainFile,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("sync error: %s", resp.Error)
	}

	const numQueries = 50
	start := time.Now()

	for i := range numQueries {
		resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
			Method: goplscli.MethodDefinition,
			File:   mainFile,
			Line:   11,
			Column: 9,
		})
		if err != nil {
			t.Fatalf("query %d: request error: %v", i, err)
		}
		if resp.Error != "" {
			t.Fatalf("query %d: response error: %s", i, resp.Error)
		}
		if len(resp.Locations) == 0 {
			t.Fatalf("query %d: no locations returned", i)
		}
		if resp.Locations[0].Start.Line != 6 {
			t.Errorf("query %d: got line %d, want 6", i, resp.Locations[0].Start.Line)
		}
	}

	elapsed := time.Since(start)
	t.Logf("Completed %d queries in %v (avg %v/query)", numQueries, elapsed, elapsed/numQueries)
}

// startTestServerShort is like startTestServer but uses a shorter socket path
// to avoid exceeding the Unix socket path length limit (104 bytes on macOS)
// when test names are long.
func startTestServerShort(t *testing.T, c *cache.Cache) string {
	t.Helper()

	sockDir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	addr := filepath.Join(sockDir, "s.sock")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	go func() {
		goplscli.Serve(ctx, addr, c)
	}()

	for i := range 50 {
		if _, err := os.Stat(addr); err == nil {
			break
		}
		if i == 49 {
			t.Fatal("timed out waiting for server socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr
}

// createModule creates a temporary Go module with the given files.
// Returns the absolute path to the module root.
func createModule(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	for fname, content := range files {
		path := filepath.Join(root, fname)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

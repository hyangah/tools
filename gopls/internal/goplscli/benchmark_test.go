// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// benchFiles creates a temporary Go module with test source files.
// Accepts testing.TB so it works for both tests and benchmarks.
func benchFiles(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()

	// Resolve symlinks (gopls expands them internally).
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		tb.Fatal(err)
	}

	files := map[string]string{
		"go.mod": "module example.com/test\n\ngo 1.21\n",
		"main.go": `package main

import "fmt"

// Greeting returns a greeting for the given name.
func Greeting(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	msg := Greeting("world")
	fmt.Println(msg)
}
`,
	}

	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

// benchSession creates a fully warmed GoSession: cache, session, view,
// and file synced. The session is cleaned up when the benchmark finishes.
func benchSession(b *testing.B) (*goplscli.GoSession, string) {
	b.Helper()
	ctx := b.Context()
	root := benchFiles(b)

	c := cache.New(nil)
	sess := cache.NewSession(ctx, c)

	opts := settings.DefaultOptions()
	folderURI := protocol.URIFromPath(root)
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		b.Fatalf("FetchGoEnv: %v", err)
	}

	folder := &cache.Folder{
		Dir:     folderURI,
		Name:    filepath.Base(root),
		Options: opts,
		Env:     *env,
	}

	_, _, release, err := sess.NewView(ctx, folder)
	if err != nil {
		b.Fatalf("NewView: %v", err)
	}
	release()

	gs := goplscli.NewGoSession(root, sess)

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		b.Fatalf("EnsureSynced: %v", err)
	}

	b.Cleanup(func() { gs.Close(ctx) })
	return gs, root
}

// BenchmarkDefinitionCold measures cold-start definition: create cache,
// session, view, sync file, and run a definition query.
//
// Representative result: ~94ms/op (Apple M2).
func BenchmarkDefinitionCold(b *testing.B) {
	for b.Loop() {
		ctx := b.Context()
		root := benchFiles(b)

		c := cache.New(nil)
		sess := cache.NewSession(ctx, c)

		opts := settings.DefaultOptions()
		folderURI := protocol.URIFromPath(root)
		env, err := cache.FetchGoEnv(ctx, folderURI, opts)
		if err != nil {
			b.Fatalf("FetchGoEnv: %v", err)
		}

		folder := &cache.Folder{
			Dir:     folderURI,
			Name:    filepath.Base(root),
			Options: opts,
			Env:     *env,
		}

		_, _, release, err := sess.NewView(ctx, folder)
		if err != nil {
			b.Fatalf("NewView: %v", err)
		}
		release()

		gs := goplscli.NewGoSession(root, sess)
		mainFile := filepath.Join(root, "main.go")
		if err := gs.EnsureSynced(ctx, mainFile); err != nil {
			b.Fatalf("EnsureSynced: %v", err)
		}

		uri := protocol.URIFromPath(mainFile)
		rng := protocol.Range{
			Start: protocol.Position{Line: 10, Character: 8},
			End:   protocol.Position{Line: 10, Character: 8},
		}
		locs, err := gs.Definition(ctx, uri, rng)
		if err != nil {
			b.Fatalf("Definition: %v", err)
		}
		if len(locs) == 0 {
			b.Fatal("Definition returned no results")
		}
		gs.Close(ctx)
	}
}

// BenchmarkDefinitionWarm measures a definition query on an already-warmed
// session (file already synced, snapshot cached).
//
// Representative result: ~1.7µs/op (Apple M2).
func BenchmarkDefinitionWarm(b *testing.B) {
	gs, root := benchSession(b)
	ctx := b.Context()
	mainFile := filepath.Join(root, "main.go")
	uri := protocol.URIFromPath(mainFile)
	rng := protocol.Range{
		Start: protocol.Position{Line: 10, Character: 8},
		End:   protocol.Position{Line: 10, Character: 8},
	}

	b.ResetTimer()
	for b.Loop() {
		locs, err := gs.Definition(ctx, uri, rng)
		if err != nil {
			b.Fatalf("Definition: %v", err)
		}
		if len(locs) == 0 {
			b.Fatal("Definition returned no results")
		}
	}
}

// BenchmarkEnsureSyncedUnchanged measures EnsureSynced when the file has
// not changed (fingerprint cache hit — should be just an os.Stat).
//
// Representative result: ~1.3µs/op (Apple M2).
func BenchmarkEnsureSyncedUnchanged(b *testing.B) {
	gs, root := benchSession(b)
	ctx := b.Context()
	mainFile := filepath.Join(root, "main.go")

	b.ResetTimer()
	for b.Loop() {
		if err := gs.EnsureSynced(ctx, mainFile); err != nil {
			b.Fatalf("EnsureSynced: %v", err)
		}
	}
}

// BenchmarkEnsureSyncedChanged measures EnsureSynced when the file has
// changed (fingerprint miss — must re-read and call DidModifyFiles).
//
// Representative result: ~42µs/op (Apple M2).
func BenchmarkEnsureSyncedChanged(b *testing.B) {
	gs, root := benchSession(b)
	ctx := b.Context()
	mainFile := filepath.Join(root, "main.go")

	// Read original content for rewriting.
	original, err := os.ReadFile(mainFile)
	if err != nil {
		b.Fatalf("ReadFile: %v", err)
	}

	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		// Touch the file to change its mtime, forcing a fingerprint miss.
		// Write the same content but with a new mtime.
		if err := os.WriteFile(mainFile, original, 0644); err != nil {
			b.Fatalf("WriteFile: %v", err)
		}
		// Ensure mtime actually changes (some filesystems have 1s granularity).
		now := time.Now().Add(time.Second)
		if err := os.Chtimes(mainFile, now, now); err != nil {
			b.Fatalf("Chtimes: %v", err)
		}
		b.StartTimer()

		if err := gs.EnsureSynced(ctx, mainFile); err != nil {
			b.Fatalf("EnsureSynced: %v", err)
		}
	}
}

// BenchmarkWireProtocolRoundTrip measures the full wire protocol path:
// SendRequest to a running test server, dispatching, and responding.
//
// Representative result: ~52µs/op (Apple M2).
func BenchmarkWireProtocolRoundTrip(b *testing.B) {
	root := benchFiles(b)
	c := cache.New(nil)
	addr := startBenchServer(b, c)

	ctx := b.Context()
	mainFile := filepath.Join(root, "main.go")

	// Warm up: sync the file first.
	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodSync,
		File:   mainFile,
	})
	if err != nil {
		b.Fatalf("sync: %v", err)
	}
	if resp.Error != "" {
		b.Fatalf("sync error: %s", resp.Error)
	}

	// Verify definition works before benchmarking.
	resp, err = goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodDefinition,
		File:   mainFile,
		Line:   11,
		Column: 9,
	})
	if err != nil {
		b.Fatalf("definition warmup: %v", err)
	}
	if resp.Error != "" {
		b.Fatalf("definition warmup error: %s", resp.Error)
	}

	b.ResetTimer()
	for b.Loop() {
		resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
			Method: goplscli.MethodDefinition,
			File:   mainFile,
			Line:   11,
			Column: 9,
		})
		if err != nil {
			b.Fatalf("definition: %v", err)
		}
		if resp.Error != "" {
			b.Fatalf("definition error: %s", resp.Error)
		}
		if len(resp.Locations) == 0 {
			b.Fatal("definition returned no locations")
		}
	}
}

// startBenchServer starts a CLI protocol server for benchmarks.
func startBenchServer(tb testing.TB, c *cache.Cache) string {
	tb.Helper()

	sockDir := tb.TempDir()
	addr := filepath.Join(sockDir, "cli.sock")

	ctx, cancel := context.WithCancel(tb.Context())
	tb.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		errCh <- goplscli.Serve(ctx, addr, c)
	}()

	// Wait for the socket to appear.
	for i := range 50 {
		if _, err := os.Stat(addr); err == nil {
			break
		}
		if i == 49 {
			tb.Fatal("timed out waiting for server socket")
		}
		time.Sleep(10 * time.Millisecond)
	}

	return addr
}

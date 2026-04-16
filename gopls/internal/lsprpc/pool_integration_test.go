// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsprpc

import (
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/test/integration/fake"
	"golang.org/x/tools/internal/jsonrpc2/servertest"
	"golang.org/x/tools/internal/testenv"
)

const poolTestProgram = `
-- go.mod --
module example.com/pooltest

go 1.21
-- main.go --
package main

import "fmt"

func Hello() string {
	return "hello"
}

func main() {
	fmt.Println(Hello())
}
`

// TestSessionPoolReuse verifies that when session pooling is enabled,
// the second LSP connection to the same workspace reuses the warm session
// and does not trigger a new IWL.
func TestSessionPoolReuse(t *testing.T) {
	testenv.NeedsTool(t, "go")

	sb, err := fake.NewSandbox(&fake.SandboxConfig{Files: fake.UnpackTxt(poolTestProgram)})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	ss := NewStreamServer(cache.New(nil), false, nil)
	ss.EnableSessionPool(time.Minute)
	ts := servertest.NewPipeServer(ss, nil)
	defer checkClose(t, ts.Close)

	ctx := t.Context()

	// First connection: cold start.
	ed1, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}

	// Open a file — this blocks until IWL completes, ensuring the
	// session is fully initialized and registered in the pool.
	if err := ed1.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	loc := protocol.Location{
		URI: ed1.DocumentURI("main.go"),
		Range: protocol.Range{
			Start: protocol.Position{Line: 9, Character: 14}, // Hello() call
			End:   protocol.Position{Line: 9, Character: 19},
		},
	}
	locs, err := ed1.Definitions(ctx, loc)
	if err != nil {
		t.Fatalf("Definitions on first connection: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definitions returned no results on first connection")
	}
	// Hello is defined on line 5 (0-based: 4).
	if got := locs[0].Range.Start.Line; got != 4 {
		t.Errorf("Definition line = %d, want 4", got)
	}

	// After a successful query, the session should be in the pool.
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after first connection = %d, want 1", got)
	}

	// Close first connection.
	if err := ed1.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Pool should still have the session (idle, not evicted).
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after disconnect = %d, want 1", got)
	}

	// Second connection: should reuse the warm session.
	ed2, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer ed2.Close(ctx)

	// Open and query again.
	if err := ed2.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	loc2 := protocol.Location{
		URI: ed2.DocumentURI("main.go"),
		Range: protocol.Range{
			Start: protocol.Position{Line: 9, Character: 14},
			End:   protocol.Position{Line: 9, Character: 19},
		},
	}
	locs2, err := ed2.Definitions(ctx, loc2)
	if err != nil {
		t.Fatalf("Definitions on second connection: %v", err)
	}
	if len(locs2) == 0 {
		t.Fatal("Definitions returned no results on second connection")
	}
	if got := locs2[0].Range.Start.Line; got != 4 {
		t.Errorf("Definition line on reuse = %d, want 4", got)
	}

	// Pool should still have exactly one entry.
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after reuse = %d, want 1", got)
	}
}

// TestSessionPoolEvictionIntegration verifies idle eviction with a real
// LSP connection.
func TestSessionPoolEvictionIntegration(t *testing.T) {
	testenv.NeedsTool(t, "go")

	sb, err := fake.NewSandbox(&fake.SandboxConfig{Files: fake.UnpackTxt(poolTestProgram)})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	ss := NewStreamServer(cache.New(nil), false, nil)
	ss.EnableSessionPool(100 * time.Millisecond)
	ts := servertest.NewPipeServer(ss, nil)
	defer checkClose(t, ts.Close)

	ctx := t.Context()

	ed, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ed.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for eviction.
	time.Sleep(300 * time.Millisecond)

	if got := ss.pool.len(); got != 0 {
		t.Errorf("pool.len() after eviction = %d, want 0", got)
	}
}

// TestSessionPoolDisabled verifies that without EnableSessionPool,
// the server works normally with no pool.
func TestSessionPoolDisabled(t *testing.T) {
	testenv.NeedsTool(t, "go")

	sb, err := fake.NewSandbox(&fake.SandboxConfig{Files: fake.UnpackTxt(poolTestProgram)})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	ss := NewStreamServer(cache.New(nil), false, nil)
	// NOT calling EnableSessionPool
	ts := servertest.NewPipeServer(ss, nil)
	defer checkClose(t, ts.Close)

	ctx := t.Context()
	ed, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}

	// Definition should work without pooling.
	if err := ed.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	loc := protocol.Location{
		URI: ed.DocumentURI("main.go"),
		Range: protocol.Range{
			Start: protocol.Position{Line: 9, Character: 14},
			End:   protocol.Position{Line: 9, Character: 19},
		},
	}
	locs, err := ed.Definitions(ctx, loc)
	if err != nil {
		t.Fatalf("Definitions without pool: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definitions returned no results")
	}

	if err := ed.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if ss.pool != nil {
		t.Error("pool should be nil when not enabled")
	}
}

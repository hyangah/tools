// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsprpc

import (
	"context"
	"os"
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

// poolDiskEditProgram is a two-file workspace where main.go references a
// symbol defined in lib.go. Cross-file Definition in connection 1 causes
// the server to read lib.go into snapshot.files — making it the class of
// URI the file-watcher audit calls the "worst offender": a file that is
// never opened by any editor yet is cached in the session's snapshot.
const poolDiskEditProgram = `
-- go.mod --
module example.com/pooldisktest

go 1.21
-- main.go --
package main

func Use() int { return X }
-- lib.go --
package main

var X int
`

// TestSessionPoolDiskEditBetweenConnections is the Stage 0 regression test
// for session-scoped file watchers. It exercises the gap the v4
// per-connection watcher design cannot cover: a disk edit that occurs
// after connection 1 shuts down but before connection 2 attaches to the
// same pooled session.
//
// Expected behavior: fails on lspbroker-v4 and earlier; passes after
// Stage 1 moves file-watcher ownership from *server to pooledSession so
// that the watcher survives across connections and invalidates the
// session's snapshot when disk content changes in the gap.
//
// Why Definition on a cross-file reference: conn1 walks from main.go to
// lib.go, which the snapshot caches. Since lib.go is never sent via
// didOpen by either editor, only a server-side watcher event can
// invalidate that cached handle. See research/FILE_WATCHER_AUDIT.md.
func TestSessionPoolDiskEditBetweenConnections(t *testing.T) {
	testenv.NeedsTool(t, "go")

	sb, err := fake.NewSandbox(&fake.SandboxConfig{Files: fake.UnpackTxt(poolDiskEditProgram)})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	ss := NewStreamServer(cache.New(nil), false, nil)
	ss.EnableSessionPool(time.Minute)
	ts := servertest.NewPipeServer(ss, nil)
	defer checkClose(t, ts.Close)

	ctx := t.Context()

	// Enable the server-side fsnotify watcher: the gopls default is "off",
	// so without this the pool-scoped watcher never exists and no disk-edit
	// invalidation can happen. Stage 1 depends on the watcher running.
	editorCfg := fake.EditorConfig{
		Settings: map[string]any{"fileWatcher": "fsnotify"},
	}

	// Connection 1: open main.go, resolve X. The Definition walk pulls
	// lib.go into snapshot.files; Hover on the same reference forces a
	// parse of the defining package, guaranteeing the cached handle.
	ed1, err := fake.NewEditor(sb, editorCfg).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ed1.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	xRef, err := ed1.RegexpSearch("main.go", `\bX\b`)
	if err != nil {
		t.Fatalf("locating X reference: %v", err)
	}
	defs1, err := ed1.Definitions(ctx, xRef)
	if err != nil {
		t.Fatalf("Definitions on first connection: %v", err)
	}
	if len(defs1) == 0 {
		t.Fatal("Definitions returned no results on first connection")
	}
	if got, want := defs1[0].URI, ed1.DocumentURI("lib.go"); got != want {
		t.Fatalf("Definitions URI on first connection = %s, want %s", got, want)
	}
	origLine := defs1[0].Range.Start.Line
	if _, _, err := ed1.Hover(ctx, xRef); err != nil {
		t.Fatalf("Hover on first connection: %v", err)
	}

	if got := ss.pool.len(); got != 1 {
		t.Fatalf("pool.len() after first connection = %d, want 1", got)
	}
	if err := ed1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// The session must remain pooled across the gap. If eviction happened
	// here the next connection would cold-start and pick up the new disk
	// content trivially — a false pass that would mask the bug.
	if got := ss.pool.len(); got != 1 {
		t.Fatalf("pool.len() after disconnect = %d, want 1 (session must still be pooled for this test to be meaningful)", got)
	}

	// Disk edit: shift X's declaration by 2 lines. Written via os.WriteFile,
	// not Workdir.WriteFile, so no client-side watcher notification fires
	// (there is no attached editor anyway). Only a server-side watcher
	// living past conn1's shutdown could observe this — which is exactly
	// the invariant Stage 1 establishes.
	const newLibContents = `package main



var X int
`
	if err := os.WriteFile(sb.Workdir.AbsPath("lib.go"), []byte(newLibContents), 0644); err != nil {
		t.Fatal(err)
	}
	// fsnotify debounces events for 500ms (filewatcher.fsnotifyInterval);
	// wait for the pool watcher to observe the disk edit and invalidate
	// the session's snapshot before conn2 queries. Without this, conn2's
	// Definitions can race the debounce window and see pre-edit content.
	time.Sleep(700 * time.Millisecond)
	const wantShift = 2 // X moves from line 2 to line 4 (0-based).

	// Connection 2: pool-hit on the same cache.Session. Its snapshot still
	// holds lib.go's pre-edit handle.
	ed2, err := fake.NewEditor(sb, editorCfg).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer ed2.Close(ctx)
	if err := ed2.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	xRef2, err := ed2.RegexpSearch("main.go", `\bX\b`)
	if err != nil {
		t.Fatalf("locating X reference on second connection: %v", err)
	}
	defs2, err := ed2.Definitions(ctx, xRef2)
	if err != nil {
		t.Fatalf("Definitions on second connection: %v", err)
	}
	if len(defs2) == 0 {
		t.Fatal("Definitions returned no results on second connection")
	}
	if got, want := defs2[0].URI, ed2.DocumentURI("lib.go"); got != want {
		t.Fatalf("Definitions URI on second connection = %s, want %s", got, want)
	}
	gotLine := defs2[0].Range.Start.Line
	wantLine := origLine + wantShift
	if gotLine != wantLine {
		t.Errorf("Definitions line after between-connection disk edit = %d, want %d (shift of %d from original line %d); "+
			"stale lib.go handle survived across the pooled session. "+
			"See research/FILE_WATCHER_AUDIT.md for Stage 0 / Stage 1 rationale.",
			gotLine, wantLine, wantShift, origLine)
	}

	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after reuse = %d, want 1", got)
	}
}

// TestSessionPoolWatcherFanOutIntegration exercises Stage 3c′: the
// pool-scoped file watcher must fan disk-edit events out to currently
// attached push-diagnostic subscribers, restoring the
// publishDiagnostics behavior that Stage 1 explicitly regressed.
//
// The disk edit must target a file that is *not* open in the editor —
// for an open file, the session's overlay takes precedence over disk
// content and no re-diagnose fires. We open main.go (which references
// lib.X) and then break lib.go on disk; the watcher must fan out, the
// re-diagnose must run, and publishDiagnostics for main.go (which now
// has an unresolved-symbol error from the broken lib package) must
// reach the editor.
func TestSessionPoolWatcherFanOutIntegration(t *testing.T) {
	testenv.NeedsTool(t, "go")

	const program = `
-- go.mod --
module example.com/fanouttest

go 1.21
-- main.go --
package main

import "fmt"

func main() {
	fmt.Println(X)
}
-- lib.go --
package main

var X = 1
`

	sb, err := fake.NewSandbox(&fake.SandboxConfig{Files: fake.UnpackTxt(program)})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	ss := NewStreamServer(cache.New(nil), false, nil)
	ss.EnableSessionPool(time.Minute)
	ts := servertest.NewPipeServer(ss, nil)
	defer checkClose(t, ts.Close)

	// Channel of (uri, len(diagnostics)) for every publishDiagnostics the
	// editor receives. Buffered so the server isn't blocked.
	type pubEvent struct {
		uri  protocol.DocumentURI
		ndia int
	}
	pubs := make(chan pubEvent, 64)
	hooks := fake.ClientHooks{
		OnDiagnostics: func(_ context.Context, params *protocol.PublishDiagnosticsParams) error {
			pubs <- pubEvent{uri: params.URI, ndia: len(params.Diagnostics)}
			return nil
		},
	}

	// fileWatcher: fsnotify (default is "off"); the pool only creates a
	// watcher when a non-Off mode is requested.
	editorCfg := fake.EditorConfig{
		Settings: map[string]any{"fileWatcher": "fsnotify"},
	}
	ed, err := fake.NewEditor(sb, editorCfg).Connect(t.Context(), ts, hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer ed.Close(t.Context())

	if err := ed.OpenFile(t.Context(), "main.go"); err != nil {
		t.Fatal(err)
	}

	mainURI := ed.DocumentURI("main.go")

	// Drain the initial publishDiagnostics burst (clean main.go).
	deadline := time.After(5 * time.Second)
draining:
	for {
		select {
		case <-pubs:
		case <-time.After(300 * time.Millisecond):
			break draining
		case <-deadline:
			t.Fatal("timed out draining initial publishDiagnostics")
		}
	}

	// External disk edit on lib.go (which is *not* opened by the editor —
	// no overlay shadows the disk content). Removing X breaks main.go's
	// reference, so a successful fan-out + re-diagnose produces a non-empty
	// publishDiagnostics for main.go.
	const brokenLib = "package main\n"
	if err := os.WriteFile(sb.Workdir.AbsPath("lib.go"), []byte(brokenLib), 0644); err != nil {
		t.Fatal(err)
	}

	// fsnotify debounces for 500ms; allow up to ~10s for the watcher to
	// fire, session.DidModifyFiles to invalidate, the fan-out callback to
	// kick the diagnose goroutine, and publishDiagnostics to land on the
	// editor.
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-pubs:
			if ev.uri == mainURI && ev.ndia > 0 {
				return // restored fan-out fired; test passes.
			}
		case <-timeout:
			t.Fatalf("did not receive non-empty publishDiagnostics for %s after disk edit (Stage 3c′ fan-out not wired)", mainURI)
		}
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

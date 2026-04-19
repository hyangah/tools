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

// TestSessionPoolDiskEditBetweenConnections exercises the gap where a disk
// edit occurs after connection 1 shuts down but before connection 2 attaches
// to the same pooled session. The pool-scoped watcher must survive across
// connections and invalidate the session's snapshot when disk content changes
// in the gap.
//
// Why Definition on a cross-file reference: conn1 walks from main.go to
// lib.go, which the snapshot caches. Since lib.go is never sent via
// didOpen by either editor, only a server-side watcher event can
// invalidate that cached handle.
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
	// invalidation can happen.
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
	// (there is no attached editor anyway). Only the pool-scoped watcher,
	// which survives across connections, can observe this disk edit and
	// invalidate the snapshot.
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
			"stale lib.go handle survived across the pooled session: "+
			"pool-scoped watcher did not invalidate the snapshot in the gap between connections",
			gotLine, wantLine, wantShift, origLine)
	}

	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after reuse = %d, want 1", got)
	}
}

// TestSessionPoolWatcherFanOutIntegration verifies that the pool-scoped
// file watcher fans disk-edit events out to currently attached
// push-diagnostic subscribers. When a disk edit occurs on a file that
// is not open in the editor (no overlay), the watcher event must
// trigger session invalidation, kick a re-diagnose pass, and deliver
// publishDiagnostics to any attached editor.
//
// We open main.go (which references lib.X) and then break lib.go on
// disk; the fan-out must fire, the re-diagnose must run, and
// publishDiagnostics for main.go (which now has an unresolved-symbol
// error from the broken lib package) must reach the editor.
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
			t.Fatalf("did not receive non-empty publishDiagnostics for %s after disk edit (pool-scoped watcher fan-out not wired)", mainURI)
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

// TestSessionPoolConcurrentClients verifies that two LSP connections
// attached simultaneously to the same workspace root share a single pool
// entry (pool.len()==1) and that the internal reference count reaches 2
// while both connections are open.  Both clients must be able to serve
// Definition queries from the shared session, and the pool entry should
// return to refCount==1 after the first client disconnects and to idle
// (refCount==0, pool.len()==1) after both disconnect.
func TestSessionPoolConcurrentClients(t *testing.T) {
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

	// poolRefCount reads the reference count of the (sole) pool entry,
	// holding pool.mu for safety.  It fails the test if the pool doesn't
	// have exactly one entry.
	poolRefCount := func(t *testing.T) int {
		t.Helper()
		ss.pool.mu.Lock()
		defer ss.pool.mu.Unlock()
		if got := len(ss.pool.sessions); got != 1 {
			t.Errorf("pool has %d entries, want 1", got)
			return -1
		}
		for _, ps := range ss.pool.sessions {
			return ps.refCount
		}
		return -1
	}

	defLoc := func(ed *fake.Editor) protocol.Location {
		return protocol.Location{
			URI: ed.DocumentURI("main.go"),
			Range: protocol.Range{
				Start: protocol.Position{Line: 9, Character: 14}, // Hello() call
				End:   protocol.Position{Line: 9, Character: 19},
			},
		}
	}

	// First connection: cold start.
	ed1, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ed1.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}

	// Verify first connection's Definition works.
	locs1, err := ed1.Definitions(ctx, defLoc(ed1))
	if err != nil {
		t.Fatalf("Definitions on first connection: %v", err)
	}
	if len(locs1) == 0 || locs1[0].Range.Start.Line != 4 {
		t.Fatalf("unexpected Definitions result on first connection: %v", locs1)
	}

	// Pool should have one entry with refCount==1 now.
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after first connection = %d, want 1", got)
	}
	if got := poolRefCount(t); got != 1 {
		t.Errorf("refCount with one client = %d, want 1", got)
	}

	// Second connection: warm path — ed1 is still attached.
	ed2, err := fake.NewEditor(sb, fake.EditorConfig{}).Connect(ctx, ts, fake.ClientHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ed2.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}

	// Both connections are simultaneously active; pool must still have
	// exactly one entry (they share the same session) with refCount==2.
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() with two concurrent clients = %d, want 1", got)
	}
	if got := poolRefCount(t); got != 2 {
		t.Errorf("refCount with two concurrent clients = %d, want 2", got)
	}

	// Both clients must be able to serve queries from the shared session.
	locs2, err := ed2.Definitions(ctx, defLoc(ed2))
	if err != nil {
		t.Fatalf("Definitions on second concurrent connection: %v", err)
	}
	if len(locs2) == 0 || locs2[0].Range.Start.Line != 4 {
		t.Fatalf("unexpected Definitions result on second connection: %v", locs2)
	}

	// Close first client: refCount drops to 1, pool entry stays.
	if err := ed1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after first client closes = %d, want 1", got)
	}
	if got := poolRefCount(t); got != 1 {
		t.Errorf("refCount after first client closes = %d, want 1", got)
	}

	// Close second client: refCount drops to 0, idle timer starts.
	if err := ed2.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Session stays pooled (idle) — not yet evicted.
	if got := ss.pool.len(); got != 1 {
		t.Errorf("pool.len() after both clients close = %d, want 1 (idle, not evicted)", got)
	}
}

// TestSessionPoolConcurrentDiagnosticFanOut exercises the fan-out subscriber
// set with two clients simultaneously attached. An external disk edit to a
// non-open file triggers re-diagnosis; both clients (each a push-diagnostics
// subscriber) must receive publishDiagnostics for the affected file.
func TestSessionPoolConcurrentDiagnosticFanOut(t *testing.T) {
	testenv.NeedsTool(t, "go")

	const program = `
-- go.mod --
module example.com/concurrentfanout

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

	// pubsA/pubsB receive (uri, nDiags) pairs from publishDiagnostics
	// callbacks for ed1 and ed2 respectively.
	type pubEvent struct {
		uri  protocol.DocumentURI
		ndia int
	}
	pubsA := make(chan pubEvent, 64)
	pubsB := make(chan pubEvent, 64)

	makeHooks := func(ch chan pubEvent) fake.ClientHooks {
		return fake.ClientHooks{
			OnDiagnostics: func(_ context.Context, params *protocol.PublishDiagnosticsParams) error {
				ch <- pubEvent{uri: params.URI, ndia: len(params.Diagnostics)}
				return nil
			},
		}
	}

	editorCfg := fake.EditorConfig{
		Settings: map[string]any{"fileWatcher": "fsnotify"},
	}

	ctx := t.Context()

	// First editor: connect and open main.go. The Hover call forces IWL to
	// complete and guarantees ed1's session is registered in the pool before
	// ed2 connects (so ed2 hits the warm acquire path, not a concurrent cold-start).
	ed1, err := fake.NewEditor(sb, editorCfg).Connect(ctx, ts, makeHooks(pubsA))
	if err != nil {
		t.Fatal(err)
	}
	defer ed1.Close(ctx)
	if err := ed1.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	// Hover on X forces IWL completion so the pool entry is registered.
	mainRef, err := ed1.RegexpSearch("main.go", `\bX\b`)
	if err != nil {
		t.Fatalf("locating X reference: %v", err)
	}
	if _, _, err := ed1.Hover(ctx, mainRef); err != nil {
		t.Fatalf("Hover on first connection: %v", err)
	}
	if got := ss.pool.len(); got != 1 {
		t.Fatalf("pool.len() after first editor = %d, want 1", got)
	}

	// Second editor: connects while ed1 is still attached — warm acquire path.
	ed2, err := fake.NewEditor(sb, editorCfg).Connect(ctx, ts, makeHooks(pubsB))
	if err != nil {
		t.Fatal(err)
	}
	defer ed2.Close(ctx)
	if err := ed2.OpenFile(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}

	// Both should share one pool entry.
	if got := ss.pool.len(); got != 1 {
		t.Fatalf("pool.len() with two concurrent clients = %d, want 1", got)
	}

	mainURI := ed1.DocumentURI("main.go")

	// Drain the initial publishDiagnostics bursts from both clients.
	drainDeadline := time.After(5 * time.Second)
draining:
	for {
		select {
		case <-pubsA:
		case <-pubsB:
		case <-time.After(300 * time.Millisecond):
			break draining
		case <-drainDeadline:
			t.Fatal("timed out draining initial publishDiagnostics")
		}
	}

	// Disk edit: remove X from lib.go — both clients' subscriptions to the
	// pool-scoped watcher should each receive a non-empty publishDiagnostics
	// for main.go (undefined: X).
	const brokenLib = "package main\n"
	if err := os.WriteFile(sb.Workdir.AbsPath("lib.go"), []byte(brokenLib), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for both clients to receive a diagnostic.  fsnotify debounces
	// for 500ms; allow up to 10s for re-diagnose + publish to complete.
	timeout := time.After(10 * time.Second)
	gotA, gotB := false, false
	for !gotA || !gotB {
		select {
		case ev := <-pubsA:
			if ev.uri == mainURI && ev.ndia > 0 {
				gotA = true
			}
		case ev := <-pubsB:
			if ev.uri == mainURI && ev.ndia > 0 {
				gotB = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for publishDiagnostics fan-out: clientA received=%v, clientB received=%v", gotA, gotB)
		}
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

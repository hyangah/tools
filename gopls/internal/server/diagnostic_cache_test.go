// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"testing"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
)

func TestDiagnosticCacheStoreFreshness(t *testing.T) {
	c := NewDiagnosticCache()
	uri := protocol.DocumentURI("file:///x.go")
	view := (*cache.View)(nil) // map key only; nil is fine

	c.store(uri, view, viewDiagnostics{snapshot: 1, version: 1})
	if got, _ := c.get(uri, view); got.snapshot != 1 {
		t.Fatalf("after first store: snapshot=%d, want 1", got.snapshot)
	}

	// Older snapshot must not overwrite.
	c.store(uri, view, viewDiagnostics{snapshot: 0, version: 0, final: true})
	if got, _ := c.get(uri, view); got.snapshot != 1 || got.version != 1 {
		t.Fatalf("older snapshot overwrote newer: got snapshot=%d version=%d", got.snapshot, got.version)
	}

	// Same snapshot, non-final must not overwrite.
	c.store(uri, view, viewDiagnostics{snapshot: 1, version: 2, final: false})
	if got, _ := c.get(uri, view); got.version != 1 {
		t.Fatalf("non-final same-snapshot overwrote: got version=%d", got.version)
	}

	// Same snapshot, final must overwrite.
	c.store(uri, view, viewDiagnostics{snapshot: 1, version: 3, final: true})
	if got, _ := c.get(uri, view); got.version != 3 || !got.final {
		t.Fatalf("final same-snapshot did not overwrite: got version=%d final=%v", got.version, got.final)
	}

	// Newer snapshot must overwrite regardless of finality.
	c.store(uri, view, viewDiagnostics{snapshot: 2, version: 4, final: false})
	if got, _ := c.get(uri, view); got.snapshot != 2 || got.version != 4 {
		t.Fatalf("newer snapshot did not overwrite: got snapshot=%d version=%d", got.snapshot, got.version)
	}
}

func TestDiagnosticCacheReconcilePrunesDeletedViews(t *testing.T) {
	c := NewDiagnosticCache()
	uri := protocol.DocumentURI("file:///x.go")

	// Use distinct dummy view pointers.
	v1 := new(cache.View)
	v2 := new(cache.View)

	c.store(uri, v1, viewDiagnostics{snapshot: 1, version: 1})
	c.store(uri, v2, viewDiagnostics{snapshot: 1, version: 1})

	// keep only v1; v2 should be pruned.
	keep := viewSet{v1: unit{}}
	allViews, byView := c.reconcile(uri, keep, 1)
	if len(allViews) != 1 || allViews[0] != v1 {
		t.Fatalf("reconcile returned views=%v, want [v1]", allViews)
	}
	if _, ok := byView[v2]; ok {
		t.Fatalf("byView returned a deleted view")
	}

	// v2 should be gone from the cache after reconcile.
	if _, ok := c.get(uri, v2); ok {
		t.Fatalf("reconcile did not prune v2 from cache")
	}

	// Wrong-version entries are skipped but not pruned.
	c.store(uri, v2, viewDiagnostics{snapshot: 1, version: 7})
	keep = viewSet{v1: unit{}, v2: unit{}}
	allViews, byView = c.reconcile(uri, keep, 1)
	if len(allViews) != 1 || allViews[0] != v1 {
		t.Fatalf("reconcile with version mismatch returned views=%v, want [v1]", allViews)
	}
	if _, ok := byView[v2]; ok {
		t.Fatalf("byView included a wrong-version entry")
	}
	if _, ok := c.get(uri, v2); !ok {
		t.Fatalf("reconcile pruned a wrong-version entry; should keep for other versions")
	}
}

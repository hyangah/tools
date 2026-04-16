// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsprpc

import (
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
)

func TestSessionPool_AcquireEmpty(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	if got := p.acquire(poolKey{root: "/project"}); got != nil {
		t.Errorf("acquire on empty pool returned %v, want nil", got)
	}
}

func TestSessionPool_RegisterAndAcquire(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)

	// Register a new session.
	got := p.register(key, session)
	if got != session {
		t.Fatalf("register returned different session")
	}
	if p.len() != 1 {
		t.Fatalf("pool len = %d, want 1", p.len())
	}

	// Release it so it's idle.
	p.release(key)

	// Acquire should return the same session.
	got = p.acquire(key)
	if got != session {
		t.Fatalf("acquire returned %v, want the registered session", got)
	}

	// Clean up.
	p.release(key)
}

func TestSessionPool_RegisterRace(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session1 := cache.NewSession(t.Context(), c)
	session2 := cache.NewSession(t.Context(), c)

	// First registration wins.
	got1 := p.register(key, session1)
	if got1 != session1 {
		t.Fatalf("first register returned wrong session")
	}

	// Second registration for the same key returns the existing session.
	got2 := p.register(key, session2)
	if got2 != session1 {
		t.Fatalf("second register returned %v, want first session", got2)
	}

	// Pool still has one entry.
	if p.len() != 1 {
		t.Fatalf("pool len = %d, want 1", p.len())
	}

	// Clean up: release both references.
	p.release(key)
	p.release(key)
}

func TestSessionPool_IdleEviction(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, 50*time.Millisecond) // short timeout for test
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)
	p.register(key, session)
	p.release(key) // refCount → 0, idle timer starts

	// Wait for eviction.
	time.Sleep(200 * time.Millisecond)

	if p.len() != 0 {
		t.Fatalf("pool len = %d after idle timeout, want 0", p.len())
	}

	// Acquire should return nil.
	if got := p.acquire(key); got != nil {
		t.Fatalf("acquire after eviction returned %v, want nil", got)
	}
}

func TestSessionPool_IdleEvictionCancelled(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, 100*time.Millisecond)
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)
	p.register(key, session)
	p.release(key) // idle timer starts

	// Acquire before timeout cancels the timer.
	time.Sleep(30 * time.Millisecond)
	got := p.acquire(key)
	if got != session {
		t.Fatalf("acquire returned %v, want session", got)
	}

	// Wait past original timeout — should NOT be evicted.
	time.Sleep(150 * time.Millisecond)
	if p.len() != 1 {
		t.Fatalf("pool len = %d, want 1 (eviction should have been cancelled)", p.len())
	}

	p.release(key)
}

func TestSessionPool_DifferentKeys(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	keyA := poolKey{root: "/project-a"}
	keyB := poolKey{root: "/project-b"}
	sessionA := cache.NewSession(t.Context(), c)
	sessionB := cache.NewSession(t.Context(), c)

	p.register(keyA, sessionA)
	p.register(keyB, sessionB)

	if p.len() != 2 {
		t.Fatalf("pool len = %d, want 2", p.len())
	}

	p.release(keyA)
	p.release(keyB)

	gotA := p.acquire(keyA)
	gotB := p.acquire(keyB)
	if gotA != sessionA {
		t.Fatalf("acquire(keyA) returned wrong session")
	}
	if gotB != sessionB {
		t.Fatalf("acquire(keyB) returned wrong session")
	}

	p.release(keyA)
	p.release(keyB)
}

func TestSessionPool_Shutdown(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)
	p.register(key, session)
	p.release(key)

	p.shutdown()

	if p.len() != 0 {
		t.Fatalf("pool len = %d after shutdown, want 0", p.len())
	}
}

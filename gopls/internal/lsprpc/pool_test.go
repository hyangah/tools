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

	if got, entry := p.acquire(poolKey{root: "/project"}); got != nil || entry != nil {
		t.Errorf("acquire on empty pool returned (%v, %v), want (nil, nil)", got, entry)
	}
}

func TestSessionPool_RegisterAndAcquire(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)

	// Register a new session.
	got, _ := p.register(key, session)
	if got != session {
		t.Fatalf("register returned different session")
	}
	if p.len() != 1 {
		t.Fatalf("pool len = %d, want 1", p.len())
	}

	// Release it so it's idle.
	p.release(key)

	// Acquire should return the same session.
	got, _ = p.acquire(key)
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
	got1, _ := p.register(key, session1)
	if got1 != session1 {
		t.Fatalf("first register returned wrong session")
	}

	// Second registration for the same key returns the existing session.
	got2, _ := p.register(key, session2)
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
	if got, _ := p.acquire(key); got != nil {
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
	got, _ := p.acquire(key)
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

	gotA, _ := p.acquire(keyA)
	gotB, _ := p.acquire(keyB)
	if gotA != sessionA {
		t.Fatalf("acquire(keyA) returned wrong session")
	}
	if gotB != sessionB {
		t.Fatalf("acquire(keyB) returned wrong session")
	}

	p.release(keyA)
	p.release(keyB)
}

func TestSessionPool_SubscribeCounter(t *testing.T) {
	c := cache.New(nil)
	p := newSessionPool(c, time.Minute)
	defer p.shutdown()

	key := poolKey{root: "/project"}
	session := cache.NewSession(t.Context(), c)
	_, entry := p.register(key, session)
	defer p.release(key)

	if entry.HasPushSubscribers() {
		t.Fatalf("fresh entry reports HasPushSubscribers = true, want false")
	}
	entry.Subscribe()
	entry.Subscribe()
	if !entry.HasPushSubscribers() {
		t.Fatalf("after two Subscribe calls HasPushSubscribers = false, want true")
	}
	entry.Unsubscribe()
	if !entry.HasPushSubscribers() {
		t.Fatalf("after 2 Subscribe + 1 Unsubscribe HasPushSubscribers = false, want true")
	}
	entry.Unsubscribe()
	if entry.HasPushSubscribers() {
		t.Fatalf("after balanced Subscribe/Unsubscribe HasPushSubscribers = true, want false")
	}
	// Defensive: extra Unsubscribe must not underflow.
	entry.Unsubscribe()
	if entry.HasPushSubscribers() {
		t.Fatalf("extra Unsubscribe left HasPushSubscribers = true, want false")
	}
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

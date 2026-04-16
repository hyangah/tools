// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsprpc

import (
	"context"
	"maps"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
)

// defaultIdleTimeout is how long a pooled session lives without any active
// connections before it is evicted.
const defaultIdleTimeout = 15 * time.Minute

// poolKey identifies a pooled session. For now it uses only the workspace
// root path; a configHash field may be added later for configuration-aware
// pooling.
type poolKey struct {
	root string // workspace root path
}

// sessionPool keeps cache.Session instances alive across LSP client
// connections, keyed by workspace root. When a new client connects and
// requests a workspace that already has a warm session, the pool returns
// it instead of creating a new one. Sessions with no active connections
// are evicted after the idle timeout.
type sessionPool struct {
	mu          sync.Mutex
	c           *cache.Cache
	idleTimeout time.Duration
	sessions    map[poolKey]*pooledSession
}

// pooledSession wraps a cache.Session with reference counting and idle
// eviction.
type pooledSession struct {
	session   *cache.Session
	refCount  int
	lastUsed  time.Time
	idleTimer *time.Timer // fires after idle timeout; nil when refCount > 0
}

// newSessionPool creates a session pool that shares the given cache.
func newSessionPool(c *cache.Cache, idleTimeout time.Duration) *sessionPool {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	return &sessionPool{
		c:           c,
		idleTimeout: idleTimeout,
		sessions:    make(map[poolKey]*pooledSession),
	}
}

// acquire returns a warm session for the given key, incrementing its
// reference count. Returns nil if no session exists for the key.
func (p *sessionPool) acquire(key poolKey) *cache.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.sessions[key]
	if !ok {
		return nil
	}
	if ps.idleTimer != nil {
		ps.idleTimer.Stop()
		ps.idleTimer = nil
	}
	ps.refCount++
	ps.lastUsed = time.Now()
	return ps.session
}

// register adds a newly created session to the pool and sets its reference
// count to 1. If a session already exists for this key (race between two
// concurrent cold starts), the existing session is returned and the caller
// should discard the one it created.
func (p *sessionPool) register(key poolKey, session *cache.Session) *cache.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ps, ok := p.sessions[key]; ok {
		// Another connection beat us to it. Use the existing session.
		if ps.idleTimer != nil {
			ps.idleTimer.Stop()
			ps.idleTimer = nil
		}
		ps.refCount++
		ps.lastUsed = time.Now()
		return ps.session
	}
	p.sessions[key] = &pooledSession{
		session:  session,
		refCount: 1,
		lastUsed: time.Now(),
	}
	return session
}

// release decrements the reference count for the given key. When the count
// reaches zero, an idle timer is started. If no new connection arrives
// before the timer fires, the session is evicted and shut down.
func (p *sessionPool) release(key poolKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.sessions[key]
	if !ok {
		return
	}
	ps.refCount--
	ps.lastUsed = time.Now()
	if ps.refCount > 0 {
		return
	}
	// Start idle timer.
	ps.idleTimer = time.AfterFunc(p.idleTimeout, func() {
		p.evict(key)
	})
}

// evict removes the session for key from the pool and shuts it down,
// but only if no new connections have arrived since the timer was set.
func (p *sessionPool) evict(key poolKey) {
	p.mu.Lock()
	ps, ok := p.sessions[key]
	if !ok || ps.refCount > 0 {
		p.mu.Unlock()
		return
	}
	delete(p.sessions, key)
	p.mu.Unlock()
	ps.session.Shutdown(context.Background())
}

// len returns the number of sessions in the pool. Useful for testing.
func (p *sessionPool) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// shutdown shuts down all pooled sessions and clears the pool.
func (p *sessionPool) shutdown() {
	p.mu.Lock()
	sessions := make(map[poolKey]*pooledSession, len(p.sessions))
	maps.Copy(sessions, p.sessions)
	clear(p.sessions)
	p.mu.Unlock()

	for _, ps := range sessions {
		if ps.idleTimer != nil {
			ps.idleTimer.Stop()
		}
		ps.session.Shutdown(context.Background())
	}
}

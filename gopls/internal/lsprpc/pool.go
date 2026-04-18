// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsprpc

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/filewatcher"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/server"
	"golang.org/x/tools/gopls/internal/settings"
	"golang.org/x/tools/internal/event"
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

// pooledSession wraps a cache.Session with reference counting, idle
// eviction, and a shared file watcher whose lifetime spans connections.
//
// The watcher's onChange closure (set once, on first EnsureWatcher) captures
// the *cache.Session — not any *server — so events continue to invalidate
// the session's snapshot after a connection shuts down. See
// kb-gopls-skills/v4/CAPABILITY_DRIVEN_PROPOSAL.md §3.3b and
// kb-gopls-skills/v4/research/FILE_WATCHER_AUDIT.md.
type pooledSession struct {
	session   *cache.Session
	refCount  int
	lastUsed  time.Time
	idleTimer *time.Timer // fires after idle timeout; nil when refCount > 0

	watcherMu   sync.Mutex
	watcher     filewatcher.Watcher // nil until first EnsureWatcher (or after closeWatcher)
	watchedDirs map[string]struct{} // cross-connection dedup for fsnotify (non-idempotent Add)

	// Push-diagnostic subscribers. Tracked so the compute path (Stage 3c)
	// can skip the diagnose goroutine when no attached connection wants
	// a publishDiagnostics fan-out. Stage 3a keeps the scaffolding only:
	// every connection increments on attach and decrements on shutdown;
	// capability-based opt-out comes in Stage 3b. See proposal §3.1.
	subsMu          sync.Mutex
	pushSubscribers int

	diagCacheOnce sync.Once
	diagCache     *server.DiagnosticCache
}

// EnsureWatcher implements server.PoolEntry. It creates the pool-scoped
// watcher on first call and is a no-op on subsequent calls (first creator's
// mode wins; later callers requesting a different mode get a warning).
func (ps *pooledSession) EnsureWatcher(ctx context.Context, mode settings.FileWatcherMode, onChange func([]protocol.FileEvent), onError func(error)) error {
	ps.watcherMu.Lock()
	defer ps.watcherMu.Unlock()
	if ps.watcher != nil {
		if ps.watcher.Mode() != mode {
			event.Log(ctx, fmt.Sprintf(
				"pool watcher mode is %q; ignoring request for %q from a later connection",
				ps.watcher.Mode(), mode))
		}
		return nil
	}
	if mode == settings.FileWatcherOff {
		return nil
	}
	w, err := filewatcher.New(mode, nil, onChange, onError)
	if err != nil {
		return err
	}
	ps.watcher = w
	ps.watchedDirs = make(map[string]struct{})
	return nil
}

// WatchDir implements server.PoolEntry.
func (ps *pooledSession) WatchDir(ctx context.Context, dir string) error {
	ps.watcherMu.Lock()
	defer ps.watcherMu.Unlock()
	if ps.watcher == nil {
		return nil
	}
	if _, ok := ps.watchedDirs[dir]; ok {
		return nil
	}
	if err := ps.watcher.WatchDir(dir); err != nil {
		return err
	}
	ps.watchedDirs[dir] = struct{}{}
	return nil
}

// Poke implements server.PoolEntry.
func (ps *pooledSession) Poke() {
	ps.watcherMu.Lock()
	defer ps.watcherMu.Unlock()
	if ps.watcher != nil {
		ps.watcher.Poke()
	}
}

// Subscribe implements server.PoolEntry. It increments the pool-scoped
// push-diagnostic subscriber count. Each attached *server that wants
// push-model publishDiagnostics calls this once on initialize.
func (ps *pooledSession) Subscribe() {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	ps.pushSubscribers++
}

// Unsubscribe implements server.PoolEntry. It decrements the subscriber
// count; callers must have previously called Subscribe exactly once.
func (ps *pooledSession) Unsubscribe() {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	if ps.pushSubscribers == 0 {
		// Defensive: never drop below zero. Should not happen if callers
		// pair Subscribe/Unsubscribe correctly.
		return
	}
	ps.pushSubscribers--
}

// HasPushSubscribers implements server.PoolEntry. Returns true if at
// least one attached connection is subscribed to push diagnostics.
func (ps *pooledSession) HasPushSubscribers() bool {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	return ps.pushSubscribers > 0
}

// DiagnosticCache implements server.PoolEntry. The cache is created on
// first call and reused for the pool entry's lifetime, so all attached
// *server connections share the same compute-results store.
func (ps *pooledSession) DiagnosticCache() *server.DiagnosticCache {
	ps.diagCacheOnce.Do(func() {
		ps.diagCache = server.NewDiagnosticCache()
	})
	return ps.diagCache
}

// closeWatcher stops and discards the pool-scoped watcher. Called by
// sessionPool.evict (and shutdown) before cache.Session.Shutdown, so
// in-flight watcher events cannot race a shutting-down session.
func (ps *pooledSession) closeWatcher() {
	ps.watcherMu.Lock()
	defer ps.watcherMu.Unlock()
	if ps.watcher == nil {
		return
	}
	if err := ps.watcher.Close(); err != nil {
		event.Error(context.Background(), "closing pool watcher", err)
	}
	ps.watcher = nil
	ps.watchedDirs = nil
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

// acquire returns a warm session for the given key along with its pool
// entry, incrementing the reference count. Returns (nil, nil) if no session
// exists for the key.
func (p *sessionPool) acquire(key poolKey) (*cache.Session, *pooledSession) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.sessions[key]
	if !ok {
		return nil, nil
	}
	if ps.idleTimer != nil {
		ps.idleTimer.Stop()
		ps.idleTimer = nil
	}
	ps.refCount++
	ps.lastUsed = time.Now()
	return ps.session, ps
}

// register adds a newly created session to the pool and sets its reference
// count to 1. Returns (winning-session, winning-entry). If a session already
// exists for this key (race between two concurrent cold starts), the winning
// session is the existing one and the caller should discard the one it
// created.
func (p *sessionPool) register(key poolKey, session *cache.Session) (*cache.Session, *pooledSession) {
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
		return ps.session, ps
	}
	ps := &pooledSession{
		session:  session,
		refCount: 1,
		lastUsed: time.Now(),
	}
	p.sessions[key] = ps
	return session, ps
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
// The pool-scoped watcher (if any) is closed before session shutdown to
// avoid racing in-flight file events against a terminating session.
func (p *sessionPool) evict(key poolKey) {
	p.mu.Lock()
	ps, ok := p.sessions[key]
	if !ok || ps.refCount > 0 {
		p.mu.Unlock()
		return
	}
	delete(p.sessions, key)
	p.mu.Unlock()
	ps.closeWatcher()
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
		ps.closeWatcher()
		ps.session.Shutdown(context.Background())
	}
}

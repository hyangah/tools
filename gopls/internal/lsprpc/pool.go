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
	"golang.org/x/tools/gopls/internal/file"
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
// The watcher's onChange closure (set once, on first EnsureWatcher)
// invalidates the *cache.Session via DidModifyFiles — even when no
// *server is currently attached, so disk edits between connections are
// not lost — and then fans the events out to per-connection subscribers
// installed via Subscribe. The closure does not capture any *server
// directly; subscribers come and go via Subscription.Close. See
// kb-gopls-skills/v4/CAPABILITY_DRIVEN_PROPOSAL.md §3.3a/§3.3b and
// kb-gopls-skills/v4/research/FILE_WATCHER_AUDIT.md.
type pooledSession struct {
	session   *cache.Session
	refCount  int
	lastUsed  time.Time
	idleTimer *time.Timer // fires after idle timeout; nil when refCount > 0

	watcherMu     sync.Mutex
	watcher       filewatcher.Watcher // nil until first EnsureWatcher (or after closeWatcher)
	watchedDirs   map[string]struct{} // cross-connection dedup for fsnotify (non-idempotent Add)
	watcherCancel context.CancelFunc  // cancels the watcher's background context; nil before EnsureWatcher

	// Push-diagnostic subscribers. Each attached *server that wants
	// push-model publishDiagnostics installs a callback via Subscribe;
	// the pool's onChange (set up below) fans watcher events out to all
	// installed callbacks after invalidating the session. The
	// HasPushSubscribers gate (Stage 3c) reads len(subscribers).
	// See proposal §3.1, §3.3a.
	subsMu      sync.Mutex
	subsNextID  uint64
	subscribers map[uint64]watcherCallback

	diagCacheOnce sync.Once
	diagCache     *server.DiagnosticCache
}

// watcherCallback is the per-subscriber hook invoked from the pool's
// shared file watcher after session.DidModifyFiles has run. The same
// signature as PoolEntry.Subscribe's callback parameter.
type watcherCallback = func(ctx context.Context, modifications []file.Modification, viewsToDiagnose map[*cache.View][]protocol.DocumentURI)

// poolSubscription is the handle returned by Subscribe; closing it
// removes the subscriber callback and drops the count. Close is
// idempotent under concurrent invocation: the closed flag is read and
// written under subsMu so two racing Closes both see one delete.
type poolSubscription struct {
	ps     *pooledSession
	id     uint64
	closed bool // guarded by ps.subsMu
}

func (sub *poolSubscription) Close() {
	sub.ps.subsMu.Lock()
	defer sub.ps.subsMu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	delete(sub.ps.subscribers, sub.id)
}

// EnsureWatcher implements server.PoolEntry. It creates the pool-scoped
// watcher on first call and is a no-op on subsequent calls (first creator's
// mode wins; later callers requesting a different mode get a warning).
//
// The onChange handler is owned by the pool: it invalidates the shared
// session via session.DidModifyFiles (so disk edits are visible across
// connections, even when no *server is attached) and then fans out the
// modifications + per-View viewsToDiagnose to every subscriber installed
// via Subscribe. The pool's background context (created here, cancelled
// on closeWatcher) is used for both calls so events outlive any one
// addFolders request. See proposal §3.3a/§3.3b.
func (ps *pooledSession) EnsureWatcher(ctx context.Context, mode settings.FileWatcherMode, onError func(error)) error {
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
	watcherCtx, cancel := context.WithCancel(context.Background())
	onChange := func(events []protocol.FileEvent) {
		modifications := make([]file.Modification, len(events))
		for i, e := range events {
			modifications[i] = file.Modification{
				URI:    e.URI,
				Action: server.ChangeTypeToFileAction(e.Type),
				OnDisk: true,
			}
		}
		viewsToDiagnose, err := ps.session.DidModifyFiles(watcherCtx, modifications)
		if err != nil {
			event.Error(watcherCtx, "pool watcher: DidModifyFiles failed", err)
			return
		}
		ps.fanOutWatcherEvents(watcherCtx, modifications, viewsToDiagnose)
	}
	w, err := filewatcher.New(mode, nil, onChange, onError)
	if err != nil {
		cancel()
		return err
	}
	ps.watcher = w
	ps.watchedDirs = make(map[string]struct{})
	ps.watcherCancel = cancel
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

// Subscribe implements server.PoolEntry. It registers cb to be invoked
// from the pool's shared watcher after session.DidModifyFiles for any
// disk events on this pool entry, and counts the subscription toward
// HasPushSubscribers (Stage 3c's compute gate). The returned
// Subscription must be Closed exactly once on connection shutdown.
func (ps *pooledSession) Subscribe(cb watcherCallback) server.Subscription {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	if ps.subscribers == nil {
		ps.subscribers = make(map[uint64]watcherCallback)
	}
	ps.subsNextID++
	id := ps.subsNextID
	ps.subscribers[id] = cb
	return &poolSubscription{ps: ps, id: id}
}

// HasPushSubscribers implements server.PoolEntry. Returns true if at
// least one attached connection is subscribed to push diagnostics.
func (ps *pooledSession) HasPushSubscribers() bool {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	return len(ps.subscribers) > 0
}

// snapshotSubscribers returns a per-call copy of the registered
// callbacks. The pool watcher uses this so it can release subsMu before
// invoking each callback (callbacks may run for milliseconds and a
// concurrent Close must not block on the watcher).
func (ps *pooledSession) snapshotSubscribers() []watcherCallback {
	ps.subsMu.Lock()
	defer ps.subsMu.Unlock()
	if len(ps.subscribers) == 0 {
		return nil
	}
	out := make([]watcherCallback, 0, len(ps.subscribers))
	for _, cb := range ps.subscribers {
		out = append(out, cb)
	}
	return out
}

// fanOutWatcherEvents calls every registered subscriber's callback for
// the given watcher batch. Called by the pool's onChange after
// session.DidModifyFiles has invalidated the session-level snapshot.
// Callbacks are invoked serially so a misbehaving subscriber cannot
// race the watcher into reordered batches; each callback is expected
// to do its heavy work on a goroutine it spawns itself (see
// (*server).onPoolWatcherEvents).
func (ps *pooledSession) fanOutWatcherEvents(ctx context.Context, modifications []file.Modification, viewsToDiagnose map[*cache.View][]protocol.DocumentURI) {
	for _, cb := range ps.snapshotSubscribers() {
		cb(ctx, modifications, viewsToDiagnose)
	}
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
// in-flight watcher events cannot race a shutting-down session. The
// watcher's background context is cancelled first so any pending
// onChange invocation observing it returns early.
func (ps *pooledSession) closeWatcher() {
	ps.watcherMu.Lock()
	defer ps.watcherMu.Unlock()
	if ps.watcher == nil {
		return
	}
	if ps.watcherCancel != nil {
		ps.watcherCancel()
		ps.watcherCancel = nil
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

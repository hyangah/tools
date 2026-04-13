// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
)

// CLIHandler manages CLI protocol connections. It maintains a pool of
// ServerSessions keyed by workspace root, sharing the gopls daemon's
// [cache.Cache]. Safe for concurrent use.
//
// Idle sessions are automatically evicted after IdleTimeout of inactivity.
// The evictor goroutine runs every EvictInterval and is stopped when
// Close is called.
type CLIHandler struct {
	cache *cache.Cache

	closeOnce sync.Once
	done      chan struct{} // closed by Close to stop the evictor

	mu sync.Mutex
	// IdleTimeout is how long a session can be idle before eviction.
	// Zero means no idle eviction. Default: 15 minutes.
	// Must be set before the first call to SessionFor or under mu.
	IdleTimeout time.Duration
	// EvictInterval is how often the evictor checks for idle sessions.
	// Default: 60 seconds.
	// Must be set before the first call to SessionFor or under mu.
	EvictInterval time.Duration
	sessions      map[string]*ServerSession // keyed by workspace root
	lastAccess    map[string]time.Time      // last access time per root
	evictStarted  bool
}

// NewCLIHandler creates a CLIHandler backed by the given shared cache.
// A background evictor goroutine is started on the first call to
// SessionFor; it removes sessions idle for longer than IdleTimeout.
// Call Close to stop the evictor.
//
// IdleTimeout and EvictInterval may be set after construction but
// before the first SessionFor call.
func NewCLIHandler(c *cache.Cache) *CLIHandler {
	return &CLIHandler{
		cache:         c,
		IdleTimeout:   15 * time.Minute,
		EvictInterval: 60 * time.Second,
		done:          make(chan struct{}),
		sessions:      make(map[string]*ServerSession),
		lastAccess:    make(map[string]time.Time),
	}
}

// SessionFor returns or creates a ServerSession for the given workspace root.
// The root must be an absolute path. Multiple goroutines may call this
// concurrently; at most one ServerSession is created per root.
func (h *CLIHandler) SessionFor(ctx context.Context, root string) (*ServerSession, error) {
	h.mu.Lock()
	if !h.evictStarted {
		h.evictStarted = true
		go h.evictLoop()
	}
	if gs, ok := h.sessions[root]; ok {
		h.lastAccess[root] = time.Now()
		h.mu.Unlock()
		return gs, nil
	}
	h.mu.Unlock()

	// Cache miss: create new session with in-process LSP server.
	gs, err := NewInProcessServer(ctx, h.cache, root)
	if err != nil {
		return nil, fmt.Errorf("create server session for %s: %w", root, err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	// Double-check: another goroutine may have created it.
	if existing, ok := h.sessions[root]; ok {
		gs.Close(ctx) // discard duplicate
		h.lastAccess[root] = time.Now()
		return existing, nil
	}
	h.sessions[root] = gs
	h.lastAccess[root] = time.Now()
	return gs, nil
}

// SessionForFile finds the workspace root for the given file and returns
// the corresponding ServerSession. It walks up from the file looking for
// go.mod or go.work; if none is found, uses the file's directory.
func (h *CLIHandler) SessionForFile(ctx context.Context, filePath string) (*ServerSession, error) {
	root, err := FindProjectRoot(filePath)
	if err != nil {
		return nil, err
	}
	return h.SessionFor(ctx, root)
}

// Close shuts down all sessions in the pool and stops the evictor.
// It is safe to call Close multiple times.
func (h *CLIHandler) Close(ctx context.Context) {
	h.closeOnce.Do(func() { close(h.done) })

	h.mu.Lock()
	sessions := make(map[string]*ServerSession, len(h.sessions))
	for k, v := range h.sessions {
		sessions[k] = v
	}
	h.sessions = make(map[string]*ServerSession)
	h.lastAccess = make(map[string]time.Time)
	h.mu.Unlock()

	for _, gs := range sessions {
		gs.Close(ctx)
	}
}

// FindProjectRoot walks up from filePath looking for go.work or go.mod.
// If none is found, returns the file's directory (for GOPATH/AdHoc mode).
// Stops at the user's home directory to prevent escaping.
func FindProjectRoot(filePath string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}

	dir := absPath
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	home, _ := os.UserHomeDir()

	// Walk up looking for go.work, then go.mod.
	for d := dir; ; d = filepath.Dir(d) {
		// Check go.work first (takes priority).
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d, nil
		}
		// Check go.mod.
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		// Stop at home directory or filesystem root.
		if d == home || d == filepath.Dir(d) {
			break
		}
	}

	// No module marker found — use file's directory (GOPATH/AdHoc).
	return dir, nil
}

// evictLoop periodically checks for idle sessions and evicts them.
// It runs until the done channel is closed.
func (h *CLIHandler) evictLoop() {
	h.mu.Lock()
	interval := h.EvictInterval
	h.mu.Unlock()
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.evictIdle()
		}
	}
}

// evictIdle evicts all sessions that have been idle longer than IdleTimeout.
func (h *CLIHandler) evictIdle() {
	h.mu.Lock()
	idleTimeout := h.IdleTimeout
	if idleTimeout <= 0 {
		h.mu.Unlock()
		return
	}
	cutoff := time.Now().Add(-idleTimeout)
	var stale []string
	for root, t := range h.lastAccess {
		if t.Before(cutoff) {
			stale = append(stale, root)
		}
	}
	h.mu.Unlock()

	ctx := context.Background()
	for _, root := range stale {
		h.EvictSession(ctx, root, cutoff)
	}
}

// EvictSession removes a session from the pool if it is still idle.
// The cutoff parameter prevents evicting sessions that were accessed
// between the idle scan and this call; pass time.Time{} to force eviction.
func (h *CLIHandler) EvictSession(ctx context.Context, root string, cutoff time.Time) {
	h.mu.Lock()
	gs, ok := h.sessions[root]
	if ok && !cutoff.IsZero() {
		if t, has := h.lastAccess[root]; has && t.After(cutoff) {
			ok = false // session was used since the idle scan
		}
	}
	if ok {
		delete(h.sessions, root)
		delete(h.lastAccess, root)
	}
	h.mu.Unlock()
	if ok {
		gs.Close(ctx)
	}
}

// Roots returns the workspace roots of all active sessions.
func (h *CLIHandler) Roots() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	roots := make([]string, 0, len(h.sessions))
	for root := range h.sessions {
		roots = append(roots, root)
	}
	return roots
}

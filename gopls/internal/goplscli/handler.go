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

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// CLIHandler manages CLI protocol connections. It maintains a pool of
// GoSessions keyed by workspace root, sharing the gopls daemon's
// [cache.Cache]. Safe for concurrent use.
type CLIHandler struct {
	cache *cache.Cache

	mu       sync.Mutex
	sessions map[string]*GoSession   // keyed by workspace root
	goenvs   map[string]*cache.GoEnv // cached FetchGoEnv results
}

// NewCLIHandler creates a CLIHandler backed by the given shared cache.
func NewCLIHandler(c *cache.Cache) *CLIHandler {
	return &CLIHandler{
		cache:    c,
		sessions: make(map[string]*GoSession),
		goenvs:   make(map[string]*cache.GoEnv),
	}
}

// SessionFor returns or creates a GoSession for the given workspace root.
// The root must be an absolute path. Multiple goroutines may call this
// concurrently; at most one GoSession is created per root.
func (h *CLIHandler) SessionFor(ctx context.Context, root string) (*GoSession, error) {
	h.mu.Lock()
	if gs, ok := h.sessions[root]; ok {
		h.mu.Unlock()
		return gs, nil
	}
	h.mu.Unlock()

	// Cache miss: create new session + view.
	goenv, err := h.fetchGoEnvCached(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("load Go environment for %s: %w", root, err)
	}

	sess := cache.NewSession(ctx, h.cache)
	folderURI := protocol.URIFromPath(root)
	opts := settings.DefaultOptions()
	folder := &cache.Folder{
		Dir:     folderURI,
		Name:    filepath.Base(root),
		Options: opts,
		Env:     *goenv,
	}

	_, _, release, err := sess.NewView(ctx, folder)
	if err != nil {
		return nil, fmt.Errorf("create view for %s: %w", root, err)
	}
	release() // release initial snapshot

	gs := NewGoSession(root, sess)

	h.mu.Lock()
	defer h.mu.Unlock()
	// Double-check: another goroutine may have created it.
	if existing, ok := h.sessions[root]; ok {
		sess.Shutdown(ctx) // discard duplicate
		return existing, nil
	}
	h.sessions[root] = gs
	return gs, nil
}

// fetchGoEnvCached returns Go environment for the given root,
// caching results to avoid repeated `go env -json` calls (~117ms each).
func (h *CLIHandler) fetchGoEnvCached(ctx context.Context, root string) (*cache.GoEnv, error) {
	h.mu.Lock()
	if env, ok := h.goenvs[root]; ok {
		h.mu.Unlock()
		return env, nil
	}
	h.mu.Unlock()

	folderURI := protocol.URIFromPath(root)
	opts := settings.DefaultOptions()
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	h.goenvs[root] = env
	h.mu.Unlock()
	return env, nil
}

// SessionForFile finds the workspace root for the given file and returns
// the corresponding GoSession. It walks up from the file looking for
// go.mod or go.work; if none is found, uses the file's directory.
func (h *CLIHandler) SessionForFile(ctx context.Context, filePath string) (*GoSession, error) {
	root, err := FindProjectRoot(filePath)
	if err != nil {
		return nil, err
	}
	return h.SessionFor(ctx, root)
}

// Close shuts down all sessions in the pool.
func (h *CLIHandler) Close(ctx context.Context) {
	h.mu.Lock()
	sessions := make(map[string]*GoSession, len(h.sessions))
	for k, v := range h.sessions {
		sessions[k] = v
	}
	h.sessions = make(map[string]*GoSession)
	h.goenvs = make(map[string]*cache.GoEnv)
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

// EvictSession removes a session from the pool. Used for idle cleanup.
func (h *CLIHandler) EvictSession(ctx context.Context, root string) {
	h.mu.Lock()
	gs, ok := h.sessions[root]
	if ok {
		delete(h.sessions, root)
		delete(h.goenvs, root)
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

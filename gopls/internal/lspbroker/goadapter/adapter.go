// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/lspclient"
	"golang.org/x/tools/gopls/internal/protocol"
)

// GoSession implements [lspbroker.Session] for Go source files. It
// lazily spawns a gopls subprocess on the first Handle call and keeps
// it alive until [GoSession.Close] is called.
//
// Phase 1: subprocess mode only ("gopls serve"). No -remote=auto.
// TODO(WS-D Phase 2): switch to "gopls -remote=auto serve" by default
// so that the broker shares an existing editor gopls daemon.
type GoSession struct {
	root string

	mu     sync.Mutex
	client *lspclient.Client // nil until first request; guarded by mu
}

// NewGoSession creates a GoSession for the given workspace root.
// No gopls subprocess is started until the first Handle call.
func NewGoSession(root string) *GoSession {
	return &GoSession{root: root}
}

// Root returns the absolute path to the workspace root directory.
func (s *GoSession) Root() string { return s.root }

// Handle dispatches a broker protocol request to the underlying gopls
// process. It is safe for concurrent use.
func (s *GoSession) Handle(ctx context.Context, method string, params []byte) ([]byte, error) {
	switch method {
	case lspbroker.DefinitionMethod:
		return s.handleDefinition(ctx, params)
	default:
		return nil, fmt.Errorf("goadapter: method not implemented: %q", method)
	}
}

// Close shuts down the gopls subprocess if one is running.
func (s *GoSession) Close() error {
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()

	if c != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5e9) // 5s
		defer cancel()
		return c.Shutdown(shutCtx)
	}
	return nil
}

// ensureClient returns the active gopls client, creating it on the
// first call. Concurrent callers serialize on mu.
func (s *GoSession) ensureClient(ctx context.Context) (*lspclient.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	rootURI := string(protocol.URIFromPath(s.root))
	c, err := lspclient.Dial(ctx, lspclient.Config{
		Command: []string{"gopls", "serve"},
		RootURI: rootURI,
	})
	if err != nil {
		return nil, fmt.Errorf("goadapter: dial gopls: %w", err)
	}
	s.client = c
	return c, nil
}

func (s *GoSession) handleDefinition(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal DefinitionParams: %w", err)
	}
	if req.File == "" {
		return nil, fmt.Errorf("goadapter: DefinitionParams.File is required")
	}

	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("goadapter: start gopls: %w", err)
	}

	// EnsureOpen syncs the file before the definition request.
	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, fmt.Errorf("goadapter: ensure open %s: %w", req.File, err)
	}

	uri := string(protocol.URIFromPath(req.File))
	// DefinitionParams uses 1-based line/char; lspclient uses 0-based.
	locs, err := c.Definition(ctx, uri, uint32(req.Line-1), uint32(req.Character-1))
	if err != nil {
		return nil, fmt.Errorf("goadapter: definition: %w", err)
	}
	return json.Marshal(convertLocations(locs))
}

// convertLocations converts a slice of [protocol.Location] to the broker
// wire type [lspbroker.Location].
func convertLocations(locs []protocol.Location) []lspbroker.Location {
	if len(locs) == 0 {
		return nil
	}
	out := make([]lspbroker.Location, len(locs))
	for i, l := range locs {
		out[i] = lspbroker.Location{
			URI: string(l.URI),
			Range: lspbroker.Range{
				Start: lspbroker.Position{
					Line:      int(l.Range.Start.Line),
					Character: int(l.Range.Start.Character),
				},
				End: lspbroker.Position{
					Line:      int(l.Range.End.Line),
					Character: int(l.Range.End.Character),
				},
			},
		}
	}
	return out
}

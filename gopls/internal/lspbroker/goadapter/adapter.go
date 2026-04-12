// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/lspclient"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/jsonrpc2"
)

// GoSession implements [lspbroker.Session] for Go source files. It
// lazily spawns a gopls subprocess on the first Handle call and keeps
// it alive until [GoSession.Close] is called.
//
// Phase 1: subprocess mode only ("gopls serve"). No -remote=auto.
// TODO(WS-D Phase 2): switch to "gopls -remote=auto serve" by default
// so that the broker shares an existing editor gopls daemon.
type GoSession struct {
	root      string
	diagStore interface {
		Update(uri string, version int32, serverID string, diags []protocol.Diagnostic)
	}
	serverID string

	mu           sync.Mutex
	client       *lspclient.Client // nil until first request; guarded by mu
	restartCount int               // number of crash recoveries performed
	maxRestarts  int               // crash recovery limit (default 3)
}

// SetDiagStore wires a diagnostics store into the session. It must be
// called before the first Handle call. The ds parameter must implement
// the Update method; pass lspbroker.DiagStore. The serverID identifies
// this session's server in the store.
//
// The parameter type is an anonymous interface to avoid a circular
// import between goadapter and lspbroker.
func (s *GoSession) SetDiagStore(ds interface {
	Update(uri string, version int32, serverID string, diags []protocol.Diagnostic)
}, serverID string) {
	s.diagStore = ds
	s.serverID = serverID
}

// NewGoSession creates a GoSession for the given workspace root.
// No gopls subprocess is started until the first Handle call.
func NewGoSession(root string) *GoSession {
	return &GoSession{root: root, maxRestarts: 3}
}

// Root returns the absolute path to the workspace root directory.
func (s *GoSession) Root() string { return s.root }

// Handle dispatches a broker protocol request to the underlying gopls
// process. It is safe for concurrent use.
func (s *GoSession) Handle(ctx context.Context, method string, params []byte) ([]byte, error) {
	switch method {
	case lspbroker.DefinitionMethod:
		return s.handleDefinition(ctx, params)
	case lspbroker.ReferencesMethod:
		return s.handleReferences(ctx, params)
	case lspbroker.HoverMethod:
		return s.handleHover(ctx, params)
	case lspbroker.ImplementationMethod:
		return s.handleImplementation(ctx, params)
	case lspbroker.DocumentSymbolMethod:
		return s.handleDocumentSymbol(ctx, params)
	case lspbroker.WorkspaceSymbolMethod:
		return s.handleWorkspaceSymbol(ctx, params)
	case lspbroker.PrepareCallHierarchyMethod:
		return s.handlePrepareCallHierarchy(ctx, params)
	case lspbroker.IncomingCallsMethod:
		return s.handleIncomingCalls(ctx, params)
	case lspbroker.OutgoingCallsMethod:
		return s.handleOutgoingCalls(ctx, params)
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

	// If the client exists but has crashed, attempt recovery.
	if s.client != nil {
		st := s.client.State()
		if st == lspclient.StateErrored || st == lspclient.StateStopped {
			if s.restartCount >= s.maxRestarts {
				return nil, lspbroker.ErrServerCrashed
			}
			// Close the old client and fall through to re-dial.
			shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.client.Shutdown(shutCtx)
			cancel()
			s.client = nil
			s.restartCount++
		} else {
			return s.client, nil
		}
	}

	rootURI := string(protocol.URIFromPath(s.root))
	c, err := lspclient.Dial(ctx, lspclient.Config{
		Command: []string{"gopls", "serve"},
		RootURI: rootURI,
	})
	if err != nil {
		return nil, fmt.Errorf("goadapter: dial gopls: %w", err)
	}
	// Register diagnostics callback.
	ds := s.diagStore
	sid := s.serverID
	c.OnDiagnostics(func(uri string, version int32, diags []protocol.Diagnostic) {
		if ds != nil {
			ds.Update(uri, version, sid, diags)
		}
	})
	s.client = c
	return c, nil
}

// Sync forces the session to re-read filePath from disk and send a
// textDocument/didChange to gopls, regardless of fingerprint.
func (s *GoSession) Sync(ctx context.Context, filePath string) error {
	c, err := s.ensureClient(ctx)
	if err != nil {
		return fmt.Errorf("goadapter: start gopls: %w", err)
	}
	return c.ForceSync(ctx, filePath, "go")
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
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	locs, err := callWithRetry(ctx, func() ([]protocol.Location, error) {
		return c.Definition(ctx, uri, uint32(req.Line-1), uint32(char))
	})
	if err != nil {
		return nil, fmt.Errorf("goadapter: definition: %w", err)
	}
	return json.Marshal(convertLocations(locs))
}

func (s *GoSession) handleReferences(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, err
	}
	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	locs, err := callWithRetry(ctx, func() ([]protocol.Location, error) {
		return c.References(ctx, uri, uint32(req.Line-1), uint32(char), true)
	})
	if err != nil {
		return nil, fmt.Errorf("goadapter: references: %w", err)
	}
	return json.Marshal(convertLocations(locs))
}

func (s *GoSession) handleHover(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, err
	}
	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	hover, err := c.Hover(ctx, uri, uint32(req.Line-1), uint32(char))
	if err != nil {
		return nil, fmt.Errorf("goadapter: hover: %w", err)
	}
	return json.Marshal(hover)
}

func (s *GoSession) handleImplementation(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, err
	}
	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	locs, err := callWithRetry(ctx, func() ([]protocol.Location, error) {
		return c.Implementation(ctx, uri, uint32(req.Line-1), uint32(char))
	})
	if err != nil {
		return nil, fmt.Errorf("goadapter: implementation: %w", err)
	}
	return json.Marshal(convertLocations(locs))
}

func (s *GoSession) handleDocumentSymbol(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DocumentSymbolParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal DocumentSymbolParams: %w", err)
	}
	if req.File == "" {
		return nil, fmt.Errorf("goadapter: DocumentSymbolParams.File is required")
	}

	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("goadapter: start gopls: %w", err)
	}

	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, fmt.Errorf("goadapter: ensure open %s: %w", req.File, err)
	}

	uri := string(protocol.URIFromPath(req.File))
	symbols, err := c.DocumentSymbol(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("goadapter: documentSymbol: %w", err)
	}
	return json.Marshal(symbols)
}

func (s *GoSession) handleWorkspaceSymbol(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.WorkspaceSymbolParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	symbols, err := c.WorkspaceSymbol(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("goadapter: workspaceSymbol: %w", err)
	}
	return json.Marshal(symbols)
}

func (s *GoSession) handlePrepareCallHierarchy(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureOpen(ctx, req.File, "go"); err != nil {
		return nil, err
	}
	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	items, err := c.PrepareCallHierarchy(ctx, uri, uint32(req.Line-1), uint32(char))
	if err != nil {
		return nil, fmt.Errorf("goadapter: prepareCallHierarchy: %w", err)
	}
	return json.Marshal(items)
}

func (s *GoSession) handleIncomingCalls(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.CallHierarchyItemParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var item protocol.CallHierarchyItem
	if err := json.Unmarshal(req.Item, &item); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal CallHierarchyItem: %w", err)
	}
	calls, err := c.IncomingCalls(ctx, item)
	if err != nil {
		return nil, fmt.Errorf("goadapter: incomingCalls: %w", err)
	}
	return json.Marshal(calls)
}

func (s *GoSession) handleOutgoingCalls(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req lspbroker.CallHierarchyItemParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var item protocol.CallHierarchyItem
	if err := json.Unmarshal(req.Item, &item); err != nil {
		return nil, fmt.Errorf("goadapter: unmarshal CallHierarchyItem: %w", err)
	}
	calls, err := c.OutgoingCalls(ctx, item)
	if err != nil {
		return nil, fmt.Errorf("goadapter: outgoingCalls: %w", err)
	}
	return json.Marshal(calls)
}

// callWithRetry retries an LSP call on ContentModified (-32801) with
// exponential backoff: 500ms, 1000ms, 2000ms (3 retries max).
func callWithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	backoffs := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond, 2000 * time.Millisecond}
	result, err := fn()
	if err == nil {
		return result, nil
	}
	for _, d := range backoffs {
		if !isContentModified(err) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(d):
		}
		result, err = fn()
		if err == nil {
			return result, nil
		}
	}
	return result, err
}

// isContentModified reports whether err is an LSP ContentModified error
// (code -32801).
func isContentModified(err error) bool {
	var wireErr *jsonrpc2.WireError
	if errors.As(err, &wireErr) {
		return wireErr.Code == lspbroker.ErrCodeContentModified
	}
	return false
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

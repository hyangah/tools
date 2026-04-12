// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker/lspclient"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/jsonrpc2"
)

// GenericSession implements [Session] for any language server configured
// via .lsp.json. It lazily spawns the server subprocess on the first
// Handle call and keeps it alive until [GenericSession.Close] is called.
type GenericSession struct {
	root      string
	cfg       *ServerConfig
	diagStore *DiagStore
	serverID  string

	mu           sync.Mutex
	client       *lspclient.Client
	restartCount int
}

// NewGenericSession creates a GenericSession for the given workspace root
// and server configuration. No subprocess is started until the first
// Handle call.
func NewGenericSession(root string, cfg *ServerConfig, diagStore *DiagStore, serverID string) *GenericSession {
	return &GenericSession{root: root, cfg: cfg, diagStore: diagStore, serverID: serverID}
}

// Root returns the absolute path to the workspace root directory.
func (s *GenericSession) Root() string { return s.root }

// Handle dispatches a broker protocol request to the underlying LSP
// server process. It is safe for concurrent use.
func (s *GenericSession) Handle(ctx context.Context, method string, params []byte) ([]byte, error) {
	switch method {
	case DefinitionMethod:
		return s.handlePositional(ctx, method, params)
	case ReferencesMethod:
		return s.handlePositional(ctx, method, params)
	case HoverMethod:
		return s.handleHover(ctx, params)
	case ImplementationMethod:
		return s.handlePositional(ctx, method, params)
	case DocumentSymbolMethod:
		return s.handleDocumentSymbol(ctx, params)
	case WorkspaceSymbolMethod:
		return s.handleWorkspaceSymbol(ctx, params)
	case PrepareCallHierarchyMethod:
		return s.handlePositional(ctx, method, params)
	case IncomingCallsMethod:
		return s.handleIncomingCalls(ctx, params)
	case OutgoingCallsMethod:
		return s.handleOutgoingCalls(ctx, params)
	default:
		return nil, fmt.Errorf("generic: method not implemented: %q", method)
	}
}

// Close shuts down the LSP server subprocess if one is running.
func (s *GenericSession) Close() error {
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()

	if c != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return c.Shutdown(shutCtx)
	}
	return nil
}

// ensureClient returns the active LSP client, creating it on the first
// call. Concurrent callers serialize on mu.
func (s *GenericSession) ensureClient(ctx context.Context) (*lspclient.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		st := s.client.State()
		if st == lspclient.StateErrored || st == lspclient.StateStopped {
			maxRestarts := s.cfg.MaxRestarts
			if maxRestarts == 0 {
				maxRestarts = 3
			}
			if s.restartCount >= maxRestarts {
				return nil, ErrServerCrashed
			}
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
	cfg := lspclient.Config{
		Command: s.cfg.Command,
		RootURI: rootURI,
	}

	// Convert env map to slice.
	for k, v := range s.cfg.Env {
		cfg.Env = append(cfg.Env, k+"="+v)
	}

	if len(s.cfg.InitializationOptions) > 0 {
		cfg.InitOptions = s.cfg.InitializationOptions
	}
	if len(s.cfg.Settings) > 0 {
		cfg.Settings = s.cfg.Settings
	}
	if s.cfg.WorkspaceFolder != "" {
		cfg.WorkspaceFolders = []protocol.WorkspaceFolder{{
			URI:  string(protocol.URIFromPath(s.cfg.WorkspaceFolder)),
			Name: "workspace",
		}}
	}
	if s.cfg.startupDuration > 0 {
		cfg.StartupTimeout = s.cfg.startupDuration
	}

	c, err := lspclient.Dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("generic: dial %v: %w", s.cfg.Command, err)
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
// textDocument/didChange to the LSP server, regardless of fingerprint.
func (s *GenericSession) Sync(ctx context.Context, filePath string) error {
	c, err := s.ensureClient(ctx)
	if err != nil {
		return err
	}
	langID := s.languageID(filePath)
	return c.ForceSync(ctx, filePath, langID)
}

// languageID returns the LSP language identifier for the given file path,
// based on the server's extensionToLanguage configuration.
func (s *GenericSession) languageID(file string) string {
	ext := filepath.Ext(file)
	if id, ok := s.cfg.ExtensionToLanguage[ext]; ok {
		return id
	}
	return ""
}

// handlePositional handles definition, references, implementation, and
// prepareCallHierarchy — any method that takes a file+line+character position.
func (s *GenericSession) handlePositional(ctx context.Context, method string, rawParams []byte) ([]byte, error) {
	var req DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal params: %w", err)
	}

	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}

	langID := s.languageID(req.File)
	if err := c.EnsureOpen(ctx, req.File, langID); err != nil {
		return nil, fmt.Errorf("generic: ensure open %s: %w", req.File, err)
	}

	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	line := uint32(req.Line - 1)
	col := uint32(char)

	switch method {
	case DefinitionMethod:
		locs, err := genericCallWithRetry(ctx, func() ([]protocol.Location, error) {
			return c.Definition(ctx, uri, line, col)
		})
		if err != nil {
			return nil, fmt.Errorf("generic: definition: %w", err)
		}
		return json.Marshal(genericConvertLocations(locs))

	case ReferencesMethod:
		locs, err := genericCallWithRetry(ctx, func() ([]protocol.Location, error) {
			return c.References(ctx, uri, line, col, true)
		})
		if err != nil {
			return nil, fmt.Errorf("generic: references: %w", err)
		}
		return json.Marshal(genericConvertLocations(locs))

	case ImplementationMethod:
		locs, err := genericCallWithRetry(ctx, func() ([]protocol.Location, error) {
			return c.Implementation(ctx, uri, line, col)
		})
		if err != nil {
			return nil, fmt.Errorf("generic: implementation: %w", err)
		}
		return json.Marshal(genericConvertLocations(locs))

	case PrepareCallHierarchyMethod:
		items, err := c.PrepareCallHierarchy(ctx, uri, line, col)
		if err != nil {
			return nil, fmt.Errorf("generic: prepareCallHierarchy: %w", err)
		}
		return json.Marshal(items)

	default:
		return nil, fmt.Errorf("generic: unexpected positional method: %q", method)
	}
}

func (s *GenericSession) handleHover(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req DefinitionParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	langID := s.languageID(req.File)
	if err := c.EnsureOpen(ctx, req.File, langID); err != nil {
		return nil, err
	}
	uri := string(protocol.URIFromPath(req.File))
	char := 0
	if req.Character != nil {
		char = *req.Character - 1
	}
	hover, err := c.Hover(ctx, uri, uint32(req.Line-1), uint32(char))
	if err != nil {
		return nil, fmt.Errorf("generic: hover: %w", err)
	}
	return json.Marshal(hover)
}

func (s *GenericSession) handleDocumentSymbol(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req DocumentSymbolParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal DocumentSymbolParams: %w", err)
	}
	if req.File == "" {
		return nil, fmt.Errorf("generic: DocumentSymbolParams.File is required")
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	langID := s.languageID(req.File)
	if err := c.EnsureOpen(ctx, req.File, langID); err != nil {
		return nil, fmt.Errorf("generic: ensure open %s: %w", req.File, err)
	}
	uri := string(protocol.URIFromPath(req.File))
	symbols, err := c.DocumentSymbol(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("generic: documentSymbol: %w", err)
	}
	return json.Marshal(symbols)
}

func (s *GenericSession) handleWorkspaceSymbol(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req WorkspaceSymbolParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	symbols, err := c.WorkspaceSymbol(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("generic: workspaceSymbol: %w", err)
	}
	return json.Marshal(symbols)
}

func (s *GenericSession) handleIncomingCalls(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req CallHierarchyItemParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var item protocol.CallHierarchyItem
	if err := json.Unmarshal(req.Item, &item); err != nil {
		return nil, fmt.Errorf("generic: unmarshal CallHierarchyItem: %w", err)
	}
	calls, err := c.IncomingCalls(ctx, item)
	if err != nil {
		return nil, fmt.Errorf("generic: incomingCalls: %w", err)
	}
	return json.Marshal(calls)
}

func (s *GenericSession) handleOutgoingCalls(ctx context.Context, rawParams []byte) ([]byte, error) {
	var req CallHierarchyItemParams
	if err := json.Unmarshal(rawParams, &req); err != nil {
		return nil, fmt.Errorf("generic: unmarshal params: %w", err)
	}
	c, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var item protocol.CallHierarchyItem
	if err := json.Unmarshal(req.Item, &item); err != nil {
		return nil, fmt.Errorf("generic: unmarshal CallHierarchyItem: %w", err)
	}
	calls, err := c.OutgoingCalls(ctx, item)
	if err != nil {
		return nil, fmt.Errorf("generic: outgoingCalls: %w", err)
	}
	return json.Marshal(calls)
}

// genericCallWithRetry retries an LSP call on ContentModified (-32801).
func genericCallWithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	backoffs := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond, 2000 * time.Millisecond}
	result, err := fn()
	if err == nil {
		return result, nil
	}
	for _, d := range backoffs {
		if !genericIsContentModified(err) {
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

func genericIsContentModified(err error) bool {
	var wireErr *jsonrpc2.WireError
	if errors.As(err, &wireErr) {
		return wireErr.Code == ErrCodeContentModified
	}
	return false
}

// genericConvertLocations converts protocol.Location to broker Location.
func genericConvertLocations(locs []protocol.Location) []Location {
	if len(locs) == 0 {
		return nil
	}
	out := make([]Location, len(locs))
	for i, l := range locs {
		out[i] = Location{
			URI: string(l.URI),
			Range: Range{
				Start: Position{
					Line:      int(l.Range.Start.Line),
					Character: int(l.Range.Start.Character),
				},
				End: Position{
					Line:      int(l.Range.End.Line),
					Character: int(l.Range.End.Character),
				},
			},
		}
	}
	return out
}

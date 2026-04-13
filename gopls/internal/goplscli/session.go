// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/server"
	"golang.org/x/tools/gopls/internal/settings"
)

// ServerSession wraps an in-process [protocol.Server] for a single workspace
// root, routing all code intelligence queries through the standard LSP server
// handler. This ensures that CLI queries follow the same code path as editor
// LSP requests.
//
// The session is shared across all CLI connections querying the same workspace
// root. Safe for concurrent use.
type ServerSession struct {
	root    string         // absolute path to workspace root
	session *cache.Session // concurrent-safe

	server protocol.Server // in-process LSP server
	client *cliClient      // minimal protocol.Client

	mu      sync.Mutex
	opened  map[protocol.DocumentURI]bool // overlay open tracking
	version atomic.Int32                  // monotonic version counter

	// fingerprints caches (mtime, size) to skip re-reading unchanged files.
	fingerprints map[protocol.DocumentURI]fileFingerprint
}

type fileFingerprint struct {
	mtime time.Time
	size  int64
}

// NewServerSession creates a ServerSession wrapping an in-process
// [protocol.Server]. The server must be initialized (Initialize + Initialized
// calls completed) before any query methods are called.
//
// Callers should use [CLIHandler.SessionFor] which handles initialization.
func NewServerSession(root string, session *cache.Session, svr protocol.Server, client *cliClient) *ServerSession {
	return &ServerSession{
		root:         root,
		session:      session,
		server:       svr,
		client:       client,
		opened:       make(map[protocol.DocumentURI]bool),
		fingerprints: make(map[protocol.DocumentURI]fileFingerprint),
	}
}

// Root returns the absolute path to the workspace root directory.
func (s *ServerSession) Root() string { return s.root }

// Session returns the underlying [cache.Session].
func (s *ServerSession) Session() *cache.Session { return s.session }

// Close shuts down the in-process LSP server and the underlying session.
// The server's Shutdown method handles session cleanup internally.
func (s *ServerSession) Close(ctx context.Context) {
	s.server.Shutdown(ctx)
	s.server.Exit(ctx)
}

// nextVersion returns a monotonically increasing version number for overlays.
func (s *ServerSession) nextVersion() int32 {
	return s.version.Add(1)
}

// syncContent sends textDocument/didOpen or textDocument/didChange to the
// in-process server. Must be called with s.mu held.
func (s *ServerSession) syncContent(ctx context.Context, uri protocol.DocumentURI, content []byte) error {
	version := s.nextVersion()
	if s.opened[uri] {
		return s.server.DidChange(ctx, &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
				Version:                version,
			},
			ContentChanges: []protocol.TextDocumentContentChangeEvent{
				{Text: string(content)},
			},
		})
	}
	err := s.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        uri,
			LanguageID: "go",
			Version:    version,
			Text:       string(content),
		},
	})
	if err == nil {
		s.opened[uri] = true
	}
	return err
}

// EnsureSynced reads a file from disk and updates the server's view via
// textDocument/didOpen or textDocument/didChange. It uses mtime+size
// fingerprints to skip unchanged files.
func (s *ServerSession) EnsureSynced(ctx context.Context, filePath string) error {
	uri := protocol.URIFromPath(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check fingerprint — skip if unchanged.
	fp := fileFingerprint{mtime: info.ModTime(), size: info.Size()}
	if s.opened[uri] {
		if prev, ok := s.fingerprints[uri]; ok && prev == fp {
			return nil // file unchanged
		}
	}

	if err := s.syncContent(ctx, uri, content); err != nil {
		return fmt.Errorf("sync %s: %w", filePath, err)
	}
	s.fingerprints[uri] = fp
	return nil
}

// ForceSync re-reads the file regardless of fingerprint.
func (s *ServerSession) ForceSync(ctx context.Context, filePath string) error {
	uri := protocol.URIFromPath(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.syncContent(ctx, uri, content); err != nil {
		return fmt.Errorf("sync %s: %w", filePath, err)
	}
	s.fingerprints[uri] = fileFingerprint{mtime: info.ModTime(), size: info.Size()}
	return nil
}

// posParams builds a TextDocumentPositionParams with the gopls Range extension.
func posParams(uri protocol.DocumentURI, rng protocol.Range) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position:     rng.Start,
		Range:        rng,
	}
}

// Definition returns the definition location(s) for the given position.
func (s *ServerSession) Definition(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) ([]protocol.Location, error) {
	return s.server.Definition(ctx, &protocol.DefinitionParams{
		TextDocumentPositionParams: posParams(uri, rng),
	})
}

// References returns all references to the identifier at the given position.
func (s *ServerSession) References(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range, includeDeclaration bool) ([]protocol.Location, error) {
	return s.server.References(ctx, &protocol.ReferenceParams{
		TextDocumentPositionParams: posParams(uri, rng),
		Context: protocol.ReferenceContext{
			IncludeDeclaration: includeDeclaration,
		},
	})
}

// Hover returns hover information for the given position.
func (s *ServerSession) Hover(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) (*protocol.Hover, error) {
	return s.server.Hover(ctx, &protocol.HoverParams{
		TextDocumentPositionParams: posParams(uri, rng),
	})
}

// Implementation returns implementations of the type/interface at the given position.
func (s *ServerSession) Implementation(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) ([]protocol.Location, error) {
	return s.server.Implementation(ctx, &protocol.ImplementationParams{
		TextDocumentPositionParams: posParams(uri, rng),
	})
}

// DocumentSymbols returns all symbols in the given file.
func (s *ServerSession) DocumentSymbols(ctx context.Context, uri protocol.DocumentURI) ([]protocol.DocumentSymbol, error) {
	result, err := s.server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return nil, err
	}
	// The server returns []DocumentSymbol (hierarchical) for Go files.
	var symbols []protocol.DocumentSymbol
	for _, item := range result {
		if ds, ok := item.(protocol.DocumentSymbol); ok {
			symbols = append(symbols, ds)
		}
	}
	return symbols, nil
}

// WorkspaceSymbols searches for symbols matching the query across all views.
func (s *ServerSession) WorkspaceSymbols(ctx context.Context, query string) ([]protocol.SymbolInformation, error) {
	return s.server.Symbol(ctx, &protocol.WorkspaceSymbolParams{
		Query: query,
	})
}

// Rename renames the identifier at the given position.
func (s *ServerSession) Rename(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range, newName string) ([]protocol.DocumentChange, error) {
	result, err := s.server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: posParams(uri, rng),
		NewName:                    newName,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return result.DocumentChanges, nil
}

// DiagnoseFile returns diagnostics for the given file via the server.
func (s *ServerSession) DiagnoseFile(ctx context.Context, uri protocol.DocumentURI) ([]protocol.Diagnostic, error) {
	report, err := s.server.Diagnostic(ctx, &protocol.DocumentDiagnosticParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		// Fall back: the server may not support pull diagnostics.
		// Check if we have cached diagnostics from push notifications.
		return s.client.getDiagnostics(uri), nil
	}
	if report == nil {
		return nil, nil
	}
	if fullReport, ok := report.Value.(protocol.RelatedFullDocumentDiagnosticReport); ok {
		return fullReport.Items, nil
	}
	return nil, nil
}

// cliClient implements [protocol.ClientCloser] as a minimal LSP client
// for the CLI's in-process server. It discards most push notifications
// but caches diagnostics and returns settings on workspace/configuration.
type cliClient struct {
	mu          sync.Mutex
	diagnostics map[protocol.DocumentURI][]protocol.Diagnostic
}

func newCLIClient() *cliClient {
	return &cliClient{
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
}

func (c *cliClient) getDiagnostics(uri protocol.DocumentURI) []protocol.Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diagnostics[uri]
}

// protocol.Client implementation — mostly no-ops.

func (c *cliClient) Close() error { return nil }

func (c *cliClient) LogTrace(context.Context, *protocol.LogTraceParams) error { return nil }

func (c *cliClient) Progress(context.Context, *protocol.ProgressParams) error { return nil }

func (c *cliClient) RegisterCapability(context.Context, *protocol.RegistrationParams) error {
	return nil
}

func (c *cliClient) UnregisterCapability(context.Context, *protocol.UnregistrationParams) error {
	return nil
}

func (c *cliClient) Event(context.Context, *any) error { return nil }

func (c *cliClient) PublishDiagnostics(_ context.Context, p *protocol.PublishDiagnosticsParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagnostics[p.URI] = p.Diagnostics
	return nil
}

func (c *cliClient) LogMessage(_ context.Context, p *protocol.LogMessageParams) error {
	if p.Type <= protocol.Warning {
		log.Printf("gopls: %s", p.Message)
	}
	return nil
}

func (c *cliClient) ShowDocument(context.Context, *protocol.ShowDocumentParams) (*protocol.ShowDocumentResult, error) {
	return &protocol.ShowDocumentResult{}, nil
}

func (c *cliClient) ShowMessage(_ context.Context, p *protocol.ShowMessageParams) error {
	if p.Type <= protocol.Warning {
		log.Printf("gopls: %s: %s", p.Type, p.Message)
	}
	return nil
}

func (c *cliClient) ShowMessageRequest(context.Context, *protocol.ShowMessageRequestParams) (*protocol.MessageActionItem, error) {
	return nil, nil
}

func (c *cliClient) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}

func (c *cliClient) ApplyEdit(context.Context, *protocol.ApplyWorkspaceEditParams) (*protocol.ApplyWorkspaceEditResult, error) {
	return &protocol.ApplyWorkspaceEditResult{Applied: false}, nil
}

func (c *cliClient) CodeLensRefresh(context.Context) error       { return nil }
func (c *cliClient) DiagnosticRefresh(context.Context) error     { return nil }
func (c *cliClient) FoldingRangeRefresh(context.Context) error   { return nil }
func (c *cliClient) InlayHintRefresh(context.Context) error      { return nil }
func (c *cliClient) InlineValueRefresh(context.Context) error    { return nil }
func (c *cliClient) SemanticTokensRefresh(context.Context) error { return nil }
func (c *cliClient) TextDocumentContentRefresh(context.Context, *protocol.TextDocumentContentRefreshParams) error {
	return nil
}

func (c *cliClient) WorkspaceFolders(context.Context) ([]protocol.WorkspaceFolder, error) {
	return nil, nil
}

func (c *cliClient) Configuration(_ context.Context, p *protocol.ParamConfiguration) ([]protocol.LSPAny, error) {
	results := make([]protocol.LSPAny, len(p.Items))
	for i, item := range p.Items {
		if item.Section != "gopls" {
			continue
		}
		// Return empty settings object.
		results[i] = map[string]any{}
	}
	return results, nil
}

// initializeServer performs the LSP initialize/initialized handshake on the
// in-process server. This must be called before any query methods.
func initializeServer(ctx context.Context, svr protocol.Server, root string) error {
	rootURI := protocol.URIFromPath(root)
	params := &protocol.ParamInitialize{}
	params.RootURI = rootURI
	params.Capabilities.Workspace.Configuration = true
	params.Capabilities.TextDocument.DocumentSymbol.HierarchicalDocumentSymbolSupport = true
	params.WorkspaceFolders = []protocol.WorkspaceFolder{
		{URI: string(rootURI), Name: filepath.Base(root)},
	}
	if _, err := svr.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := svr.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}
	return nil
}

// NewInProcessServer creates an in-process [protocol.Server] backed by the
// given shared cache. It creates a new [cache.Session] and initializes the
// server with the workspace root.
func NewInProcessServer(ctx context.Context, c *cache.Cache, root string) (*ServerSession, error) {
	sess := cache.NewSession(ctx, c)
	client := newCLIClient()
	opts := settings.DefaultOptions()
	svr := server.New(sess, client, opts)

	if err := initializeServer(ctx, svr, root); err != nil {
		sess.Shutdown(ctx)
		return nil, err
	}

	return NewServerSession(root, sess, svr, client), nil
}

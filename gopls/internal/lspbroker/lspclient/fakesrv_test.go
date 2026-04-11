// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"

	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/jsonrpc2"
)

// fakeServer is a minimal LSP server for in-process unit tests.
type fakeServer struct {
	t *testing.T

	mu          sync.Mutex
	initialized bool
	openFiles   map[string]string // uri → content
	// onDefinition is called for textDocument/definition requests.
	// Set to nil to return an empty result.
	onDefinition func(params *protocol.DefinitionParams) ([]protocol.Location, error)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	return &fakeServer{
		t:         t,
		openFiles: make(map[string]string),
	}
}

// serveConn handles the server side of a net.Conn using the fakeServer.
// It blocks until the connection closes. Call it in a goroutine.
func (s *fakeServer) serveConn(ctx context.Context, c net.Conn) {
	stream := jsonrpc2.NewHeaderStream(c)
	conn := jsonrpc2.NewConn(stream)
	conn.Go(ctx, jsonrpc2.MustReplyHandler(s.handle))
	<-conn.Done()
}

func (s *fakeServer) handle(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case "$/ping":
		// Used by tests to flush the notification queue (a round-trip Call
		// guarantees all preceding notifications have been processed).
		return reply(ctx, "pong", nil)

	case "initialize":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		result := &protocol.InitializeResult{
			Capabilities: protocol.ServerCapabilities{},
		}
		return reply(ctx, result, nil)

	case "initialized":
		return reply(ctx, nil, nil)

	case "shutdown":
		return reply(ctx, nil, nil)

	case "exit":
		return reply(ctx, nil, nil)

	case "textDocument/didOpen":
		var params protocol.DidOpenTextDocumentParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("didOpen: %w", err))
		}
		s.mu.Lock()
		s.openFiles[string(params.TextDocument.URI)] = params.TextDocument.Text
		s.mu.Unlock()
		return reply(ctx, nil, nil)

	case "textDocument/didChange":
		var params protocol.DidChangeTextDocumentParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("didChange: %w", err))
		}
		s.mu.Lock()
		if len(params.ContentChanges) > 0 {
			s.openFiles[string(params.TextDocument.URI)] = params.ContentChanges[len(params.ContentChanges)-1].Text
		}
		s.mu.Unlock()
		return reply(ctx, nil, nil)

	case "textDocument/didSave":
		return reply(ctx, nil, nil)

	case "textDocument/didClose":
		var params protocol.DidCloseTextDocumentParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("didClose: %w", err))
		}
		s.mu.Lock()
		delete(s.openFiles, string(params.TextDocument.URI))
		s.mu.Unlock()
		return reply(ctx, nil, nil)

	case "textDocument/definition":
		s.mu.Lock()
		fn := s.onDefinition
		s.mu.Unlock()
		if fn == nil {
			return reply(ctx, []protocol.Location{}, nil)
		}
		var params protocol.DefinitionParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("definition: %w", err))
		}
		locs, err := fn(&params)
		if err != nil {
			return reply(ctx, nil, err)
		}
		return reply(ctx, locs, nil)

	default:
		return reply(ctx, nil, fmt.Errorf("%w: %q", jsonrpc2.ErrMethodNotFound, req.Method()))
	}
}

// openFileContent returns the server-side content of uri, or "" if not open.
func (s *fakeServer) openFileContent(uri string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openFiles[uri]
}

// isInitialized reports whether the server received the initialize call.
func (s *fakeServer) isInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
}

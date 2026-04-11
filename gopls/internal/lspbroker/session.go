// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"context"
)

// SessionInfo carries the observable state of an active broker session,
// suitable for embedding in status responses and log messages. It does
// not include mutable state; call [Session] methods for live values.
type SessionInfo struct {
	// Root is the absolute path to the project root directory that
	// this session was opened for.
	Root string

	// PID is the operating-system process ID of the session's LSP
	// server subprocess, or 0 if no server has been started yet.
	PID int
}

// Session is a per-project state container. One Session exists for
// each workspace root that the broker has opened. WS-C (lspclient)
// fills in the full body in Phase 1; this Phase 0/1 interface is the
// minimal surface needed by [Broker].
//
// Callers obtain a Session via [Broker.sessionForFile], which creates
// one on first access.
type Session interface {
	// Root returns the absolute path to the project root directory.
	Root() string

	// Handle dispatches a single broker protocol request to the
	// appropriate language server and returns its response. The
	// method field of req carries the lsp.* method name (e.g.
	// "lsp.definition"). Handle must be safe for concurrent use.
	Handle(ctx context.Context, method string, params []byte) ([]byte, error)

	// Close shuts down the session's LSP server processes and releases
	// associated resources. It is called when the broker is stopping or
	// when the session has been idle for too long.
	Close() error
}

// stubSession is the Phase 1 placeholder returned by [newSession]. It
// satisfies the [Session] interface but does not start any LSP server.
// WS-E replaces it with a real implementation in Phase 3.
type stubSession struct {
	root string
}

func newSession(root string) Session {
	return &stubSession{root: root}
}

func (s *stubSession) Root() string { return s.root }

func (s *stubSession) Handle(_ context.Context, method string, _ []byte) ([]byte, error) {
	return nil, errMethodNotImplemented(method)
}

func (s *stubSession) Close() error { return nil }

// errMethodNotImplemented returns the standard JSON-RPC "method not
// found" error wrapping the given method name.
func errMethodNotImplemented(method string) error {
	return &rpcError{code: -32601, msg: "method not implemented in Phase 1: " + method}
}

// rpcError is a minimal JSON-RPC error value used when we cannot
// import jsonrpc2 without a circular dependency.
type rpcError struct {
	code int
	msg  string
}

func (e *rpcError) Error() string { return e.msg }

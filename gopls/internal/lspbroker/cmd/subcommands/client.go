// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/internal/jsonrpc2"
)

// BrokerConn wraps a jsonrpc2.Conn to the broker daemon and provides
// typed helpers for broker protocol calls.
type BrokerConn struct {
	conn jsonrpc2.Conn
}

// DialBroker connects to the broker daemon's unix socket at
// cacheDir/broker.sock and returns an initialized BrokerConn.
// It also performs the broker.handshake RPC; the caller can start
// making lsp.* calls immediately after DialBroker returns.
//
// selfPath is the absolute path of the running gopls binary
// (os.Executable()); it is sent in the handshake for logging.
func DialBroker(ctx context.Context, cacheDir, selfPath string) (*BrokerConn, error) {
	sockPath := filepath.Join(cacheDir, "broker.sock")

	const dialTimeout = 5 * time.Second
	netConn, err := net.DialTimeout("unix", sockPath, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial broker socket %s: %w", sockPath, err)
	}

	stream := jsonrpc2.NewHeaderStream(netConn)
	jconn := jsonrpc2.NewConn(stream)
	// Start the dispatch goroutine; the broker should not initiate
	// server-side requests but we need Go() to process responses.
	jconn.Go(ctx, jsonrpc2.MethodNotFound)

	bc := &BrokerConn{conn: jconn}
	if _, err := lspbroker.Handshake(ctx, jconn, selfPath, ""); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("broker handshake: %w", err)
	}
	return bc, nil
}

// Close closes the underlying connection.
func (bc *BrokerConn) Close() error {
	return bc.conn.Close()
}

// Stop sends a broker.stop request to the broker daemon, asking it to
// shut down gracefully.
func (bc *BrokerConn) Stop(ctx context.Context) error {
	_, err := bc.conn.Call(ctx, lspbroker.StopMethod, nil, nil)
	return err
}

// Definition sends an lsp.definition request to the broker daemon.
func (bc *BrokerConn) Definition(ctx context.Context, params lspbroker.DefinitionParams) ([]lspbroker.Location, error) {
	var locs []lspbroker.Location
	if _, err := bc.conn.Call(ctx, lspbroker.DefinitionMethod, params, &locs); err != nil {
		return nil, fmt.Errorf("lsp.definition: %w", err)
	}
	return locs, nil
}

// References sends an lsp.references request.
func (bc *BrokerConn) References(ctx context.Context, params lspbroker.DefinitionParams) ([]lspbroker.Location, error) {
	var locs []lspbroker.Location
	if _, err := bc.conn.Call(ctx, lspbroker.ReferencesMethod, params, &locs); err != nil {
		return nil, fmt.Errorf("lsp.references: %w", err)
	}
	return locs, nil
}

// Hover sends an lsp.hover request and returns the raw JSON response.
func (bc *BrokerConn) Hover(ctx context.Context, params lspbroker.DefinitionParams) (json.RawMessage, error) {
	var result json.RawMessage
	if _, err := bc.conn.Call(ctx, lspbroker.HoverMethod, params, &result); err != nil {
		return nil, fmt.Errorf("lsp.hover: %w", err)
	}
	return result, nil
}

// Implementation sends an lsp.implementation request.
func (bc *BrokerConn) Implementation(ctx context.Context, params lspbroker.DefinitionParams) ([]lspbroker.Location, error) {
	var locs []lspbroker.Location
	if _, err := bc.conn.Call(ctx, lspbroker.ImplementationMethod, params, &locs); err != nil {
		return nil, fmt.Errorf("lsp.implementation: %w", err)
	}
	return locs, nil
}

// DocumentSymbol sends an lsp.documentSymbol request.
func (bc *BrokerConn) DocumentSymbol(ctx context.Context, file string) (json.RawMessage, error) {
	params := lspbroker.DocumentSymbolParams{
		Version: lspbroker.ProtocolVersion,
		File:    file,
	}
	var result json.RawMessage
	if _, err := bc.conn.Call(ctx, lspbroker.DocumentSymbolMethod, params, &result); err != nil {
		return nil, fmt.Errorf("lsp.documentSymbol: %w", err)
	}
	return result, nil
}

// WorkspaceSymbol sends an lsp.workspaceSymbol request.
func (bc *BrokerConn) WorkspaceSymbol(ctx context.Context, query string) (json.RawMessage, error) {
	params := lspbroker.WorkspaceSymbolParams{
		Version: lspbroker.ProtocolVersion,
		Query:   query,
	}
	var result json.RawMessage
	if _, err := bc.conn.Call(ctx, lspbroker.WorkspaceSymbolMethod, params, &result); err != nil {
		return nil, fmt.Errorf("lsp.workspaceSymbol: %w", err)
	}
	return result, nil
}

// PrepareCallHierarchy sends an lsp.prepareCallHierarchy request and returns the raw JSON response.
func (bc *BrokerConn) PrepareCallHierarchy(ctx context.Context, params lspbroker.DefinitionParams) (json.RawMessage, error) {
	var result json.RawMessage
	if _, err := bc.conn.Call(ctx, lspbroker.PrepareCallHierarchyMethod, params, &result); err != nil {
		return nil, fmt.Errorf("lsp.prepareCallHierarchy: %w", err)
	}
	return result, nil
}

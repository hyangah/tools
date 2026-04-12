// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/tools/gopls/internal/protocol"
)

// Definition requests the definition location(s) for the symbol at the given
// (zero-based) line and character offset inside the document identified by
// uri. The file must already be open on the server (see [Client.EnsureOpen]).
//
// It returns a slice of [protocol.Location] values. Most servers return one
// result; some return multiple (e.g. TypeScript for overloads).
func (c *Client) Definition(ctx context.Context, uri string, line, character uint32) ([]protocol.Location, error) {
	params := &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{
				URI: protocol.DocumentURI(uri),
			},
			Position: protocol.Position{
				Line:      line,
				Character: character,
			},
		},
	}

	var raw json.RawMessage
	if _, err := c.conn.Call(ctx, "textDocument/definition", params, &raw); err != nil {
		return nil, fmt.Errorf("textDocument/definition: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// The result can be Location | Location[] | LocationLink[] per spec.
	// Try Location[] first, then a single Location.
	var locations []protocol.Location
	if err := json.Unmarshal(raw, &locations); err == nil {
		return locations, nil
	}
	var single protocol.Location
	if err := json.Unmarshal(raw, &single); err == nil {
		return []protocol.Location{single}, nil
	}
	// LocationLink[] — normalize to Location[].
	var links []protocol.LocationLink
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, fmt.Errorf("textDocument/definition: cannot decode result: %s", string(raw))
	}
	out := make([]protocol.Location, len(links))
	for i, l := range links {
		out[i] = protocol.Location{
			URI:   l.TargetURI,
			Range: l.TargetSelectionRange,
		}
	}
	return out, nil
}

// DocumentSymbol returns the document symbol tree for the file identified by uri.
// The file must already be open on the server (see [Client.EnsureOpen]).
func (c *Client) DocumentSymbol(ctx context.Context, uri string) ([]protocol.DocumentSymbol, error) {
	params := &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{
			URI: protocol.DocumentURI(uri),
		},
	}

	var raw json.RawMessage
	if _, err := c.conn.Call(ctx, "textDocument/documentSymbol", params, &raw); err != nil {
		return nil, fmt.Errorf("textDocument/documentSymbol: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// The result can be DocumentSymbol[] or SymbolInformation[] per spec.
	// Try DocumentSymbol[] first (hierarchical, preferred).
	var symbols []protocol.DocumentSymbol
	if err := json.Unmarshal(raw, &symbols); err == nil {
		return symbols, nil
	}
	// Fallback: SymbolInformation[] (flat, legacy). Convert to DocumentSymbol.
	var infos []protocol.SymbolInformation
	if err := json.Unmarshal(raw, &infos); err != nil {
		return nil, fmt.Errorf("textDocument/documentSymbol: cannot decode result: %s", string(raw))
	}
	out := make([]protocol.DocumentSymbol, len(infos))
	for i, info := range infos {
		out[i] = protocol.DocumentSymbol{
			Name:           info.Name,
			Kind:           info.Kind,
			Range:          info.Location.Range,
			SelectionRange: info.Location.Range,
		}
	}
	return out, nil
}

// References returns all references to the symbol at the given position.
func (c *Client) References(ctx context.Context, uri string, line, character uint32, includeDecl bool) ([]protocol.Location, error) {
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentURI(uri)},
			Position:     protocol.Position{Line: line, Character: character},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: includeDecl},
	}
	var locs []protocol.Location
	if _, err := c.conn.Call(ctx, "textDocument/references", params, &locs); err != nil {
		return nil, fmt.Errorf("textDocument/references: %w", err)
	}
	return locs, nil
}

// Hover returns hover information for the symbol at the given position.
func (c *Client) Hover(ctx context.Context, uri string, line, character uint32) (*protocol.Hover, error) {
	params := &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentURI(uri)},
			Position:     protocol.Position{Line: line, Character: character},
		},
	}
	var result protocol.Hover
	if _, err := c.conn.Call(ctx, "textDocument/hover", params, &result); err != nil {
		return nil, fmt.Errorf("textDocument/hover: %w", err)
	}
	return &result, nil
}

// Implementation returns the implementation location(s) for the symbol at
// the given position (e.g. concrete types implementing an interface).
func (c *Client) Implementation(ctx context.Context, uri string, line, character uint32) ([]protocol.Location, error) {
	params := &protocol.ImplementationParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentURI(uri)},
			Position:     protocol.Position{Line: line, Character: character},
		},
	}
	var raw json.RawMessage
	if _, err := c.conn.Call(ctx, "textDocument/implementation", params, &raw); err != nil {
		return nil, fmt.Errorf("textDocument/implementation: %w", err)
	}
	return decodeLocations(raw, "textDocument/implementation")
}

// PrepareCallHierarchy returns the call hierarchy item(s) at the given position.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, line, character uint32) ([]protocol.CallHierarchyItem, error) {
	params := &protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentURI(uri)},
			Position:     protocol.Position{Line: line, Character: character},
		},
	}
	var items []protocol.CallHierarchyItem
	if _, err := c.conn.Call(ctx, "textDocument/prepareCallHierarchy", params, &items); err != nil {
		return nil, fmt.Errorf("textDocument/prepareCallHierarchy: %w", err)
	}
	return items, nil
}

// IncomingCalls returns the incoming calls for a call hierarchy item.
func (c *Client) IncomingCalls(ctx context.Context, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyIncomingCall, error) {
	params := &protocol.CallHierarchyIncomingCallsParams{Item: item}
	var calls []protocol.CallHierarchyIncomingCall
	if _, err := c.conn.Call(ctx, "callHierarchy/incomingCalls", params, &calls); err != nil {
		return nil, fmt.Errorf("callHierarchy/incomingCalls: %w", err)
	}
	return calls, nil
}

// OutgoingCalls returns the outgoing calls from a call hierarchy item.
func (c *Client) OutgoingCalls(ctx context.Context, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyOutgoingCall, error) {
	params := &protocol.CallHierarchyOutgoingCallsParams{Item: item}
	var calls []protocol.CallHierarchyOutgoingCall
	if _, err := c.conn.Call(ctx, "callHierarchy/outgoingCalls", params, &calls); err != nil {
		return nil, fmt.Errorf("callHierarchy/outgoingCalls: %w", err)
	}
	return calls, nil
}

// WorkspaceSymbol searches for symbols matching the given query across the workspace.
func (c *Client) WorkspaceSymbol(ctx context.Context, query string) ([]protocol.SymbolInformation, error) {
	params := &protocol.WorkspaceSymbolParams{Query: query}
	var symbols []protocol.SymbolInformation
	if _, err := c.conn.Call(ctx, "workspace/symbol", params, &symbols); err != nil {
		return nil, fmt.Errorf("workspace/symbol: %w", err)
	}
	return symbols, nil
}

// decodeLocations decodes a Location | Location[] | LocationLink[] response.
func decodeLocations(raw json.RawMessage, method string) ([]protocol.Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var locs []protocol.Location
	if err := json.Unmarshal(raw, &locs); err == nil {
		return locs, nil
	}
	var single protocol.Location
	if err := json.Unmarshal(raw, &single); err == nil {
		return []protocol.Location{single}, nil
	}
	var links []protocol.LocationLink
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, fmt.Errorf("%s: cannot decode result: %s", method, string(raw))
	}
	out := make([]protocol.Location, len(links))
	for i, l := range links {
		out[i] = protocol.Location{URI: l.TargetURI, Range: l.TargetSelectionRange}
	}
	return out, nil
}

// ExecuteCommand is the escape hatch for workspace/executeCommand.
func (c *Client) ExecuteCommand(ctx context.Context, name string, args ...json.RawMessage) (json.RawMessage, error) {
	params := struct {
		Command   string            `json:"command"`
		Arguments []json.RawMessage `json:"arguments,omitempty"`
	}{
		Command:   name,
		Arguments: args,
	}
	var result json.RawMessage
	if _, err := c.conn.Call(ctx, "workspace/executeCommand", params, &result); err != nil {
		return nil, fmt.Errorf("workspace/executeCommand %s: %w", name, err)
	}
	return result, nil
}

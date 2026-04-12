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

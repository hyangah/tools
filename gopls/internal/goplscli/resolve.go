// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/tools/gopls/internal/protocol"
)

// DecodeDocumentSymbols converts the heterogeneous []any returned by
// server.DocumentSymbol into typed DocumentSymbol values.
//
// When the LSP response crosses a JSON-RPC boundary (e.g. -remote=auto),
// each item arrives as map[string]any rather than the concrete type, so a
// direct type assertion to protocol.DocumentSymbol fails. This helper
// re-marshals such maps and ignores SymbolInformation entries (which gopls
// only emits to clients without HierarchicalDocumentSymbolSupport).
func DecodeDocumentSymbols(items []any) ([]protocol.DocumentSymbol, error) {
	var out []protocol.DocumentSymbol
	for _, item := range items {
		switch v := item.(type) {
		case protocol.DocumentSymbol:
			out = append(out, v)
		case map[string]any:
			if _, ok := v["selectionRange"]; !ok {
				continue // SymbolInformation, not a DocumentSymbol
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			var ds protocol.DocumentSymbol
			if err := json.Unmarshal(b, &ds); err != nil {
				return nil, err
			}
			out = append(out, ds)
		}
	}
	return out, nil
}

// ResolveSymbol finds the position of a named symbol in the given file
// by querying textDocument/documentSymbol via the LSP server.
// If line > 0, it narrows to symbols on that line (disambiguation).
// The line parameter is 1-based (matching CLI conventions).
func ResolveSymbol(ctx context.Context, server protocol.Server, filePath string, symbolName string, line int) (protocol.DocumentURI, protocol.Range, error) {
	uri := protocol.URIFromPath(filePath)
	result, err := server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return "", protocol.Range{}, fmt.Errorf("document symbols: %w", err)
	}

	symbols, err := DecodeDocumentSymbols(result)
	if err != nil {
		return "", protocol.Range{}, fmt.Errorf("decode document symbols: %w", err)
	}

	// Collect all matching symbols by walking the tree.
	var matches []protocol.Range
	var walk func(syms []protocol.DocumentSymbol)
	walk = func(syms []protocol.DocumentSymbol) {
		for _, s := range syms {
			if matchesSymbolName(s.Name, symbolName) {
				matches = append(matches, s.SelectionRange)
			}
			walk(s.Children)
		}
	}
	walk(symbols)

	// Narrow by line if requested.
	if line > 0 && len(matches) > 1 {
		protoLine := uint32(line - 1) // 1-based to 0-based
		var narrowed []protocol.Range
		for _, r := range matches {
			if r.Start.Line == protoLine {
				narrowed = append(narrowed, r)
			}
		}
		if len(narrowed) > 0 {
			matches = narrowed
		}
	}

	switch len(matches) {
	case 0:
		return "", protocol.Range{}, fmt.Errorf("symbol not found: %s", symbolName)
	case 1:
		return uri, matches[0], nil
	default:
		return "", protocol.Range{}, fmt.Errorf("ambiguous symbol %q (%d matches; use FILE:LINE to narrow)", symbolName, len(matches))
	}
}

// matchesSymbolName reports whether a symbol's name matches the query.
// gopls formats methods as "(T).Name", so we match both exact name
// and the suffix after ".".
func matchesSymbolName(symbolName, query string) bool {
	if symbolName == query {
		return true
	}
	if i := strings.LastIndex(symbolName, "."); i >= 0 {
		return symbolName[i+1:] == query
	}
	return false
}

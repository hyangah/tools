// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/tools/gopls/internal/protocol"
)

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

	// Extract DocumentSymbols from the result.
	var symbols []protocol.DocumentSymbol
	for _, item := range result {
		if ds, ok := item.(protocol.DocumentSymbol); ok {
			symbols = append(symbols, ds)
		}
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

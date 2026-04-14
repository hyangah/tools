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

// ResolveSymbol finds the position of a named symbol in the given file.
// If line > 0, it narrows to symbols on that line (disambiguation).
// The line parameter is 1-based (matching CLI conventions).
// Returns the URI and the SelectionRange of the matched symbol.
//
// Matching is by exact name or suffix after ".". This handles gopls's
// method naming convention where methods appear as "(T).Name".
func ResolveSymbol(ctx context.Context, gs *GoSession, filePath string, symbolName string, line int) (protocol.DocumentURI, protocol.Range, error) {
	uri := protocol.URIFromPath(filePath)

	symbols, err := gs.DocumentSymbols(ctx, uri)
	if err != nil {
		return "", protocol.Range{}, fmt.Errorf("document symbols: %w", err)
	}

	// Collect all matching symbols by walking the tree.
	// Match both exact name and suffix after "." to handle method
	// names like "(A).Name" when searching for "Name".
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
		protoLine := uint32(line - 1) // convert 1-based to 0-based
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
		return "", protocol.Range{}, fmt.Errorf("ambiguous symbol: %s (found %d matches, use FILE:LINE to narrow)", symbolName, len(matches))
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

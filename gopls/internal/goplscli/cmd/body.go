// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"os"

	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
)

// fetchDeclBody fetches the source text of the declaration at loc by:
//  1. Calling textDocument/documentSymbol on loc.URI.
//  2. Walking the symbol tree to find the symbol whose SelectionRange
//     contains loc.Range.Start.
//  3. Reading the file on disk and returning the bytes in symbol.Range.
//
// Returns empty string (not error) when no matching symbol is found.
// Returns an error only on actual RPC or I/O failure.
func fetchDeclBody(ctx context.Context, server protocol.Server, loc protocol.Location) (string, error) {
	result, err := server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: loc.URI},
	})
	if err != nil {
		return "", err
	}
	symbols, err := goplscli.DecodeDocumentSymbols(result)
	if err != nil {
		return "", err
	}

	sym := findSymbolByPosition(symbols, loc.Range.Start)
	if sym == nil {
		return "", nil
	}

	content, err := os.ReadFile(loc.URI.Path())
	if err != nil {
		return "", err
	}

	mapper := protocol.NewMapper(loc.URI, content)
	start, end, err := mapper.RangeOffsets(sym.Range)
	if err != nil {
		return "", err
	}
	return string(content[start:end]), nil
}

// findSymbolByPosition walks the document symbol tree and returns the innermost
// symbol whose SelectionRange contains pos. Returns nil if no symbol matches.
func findSymbolByPosition(syms []protocol.DocumentSymbol, pos protocol.Position) *protocol.DocumentSymbol {
	for i := range syms {
		s := &syms[i]
		// Recurse into children first to find the innermost match.
		if child := findSymbolByPosition(s.Children, pos); child != nil {
			return child
		}
		// Check if this symbol's SelectionRange contains pos (inclusive).
		if posInRange(pos, s.SelectionRange) {
			return s
		}
	}
	return nil
}

// posInRange reports whether pos is within r (inclusive on both ends).
func posInRange(pos protocol.Position, r protocol.Range) bool {
	return protocol.ComparePosition(pos, r.Start) >= 0 &&
		protocol.ComparePosition(pos, r.End) <= 0
}

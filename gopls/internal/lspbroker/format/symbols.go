// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// documentSymbol is a minimal local struct for decoding LSP DocumentSymbol
// results. The broker passes through the raw gopls JSON; we only need
// Name, Range, and Children for the outline display.
type documentSymbol struct {
	Name     string           `json:"name"`
	Kind     int              `json:"kind"`
	Range    symbolRange      `json:"range"`
	Children []documentSymbol `json:"children"`
}

type symbolRange struct {
	Start symbolPosition `json:"start"`
}

type symbolPosition struct {
	Line int `json:"line"`
}

// Symbols writes a nested outline of document symbols to w.
// raw is the JSON array of DocumentSymbol from the broker.
// Returns false if the result is empty.
func Symbols(w io.Writer, raw json.RawMessage) (bool, error) {
	if isNullOrEmpty(raw) {
		return false, nil
	}
	var syms []documentSymbol
	if err := json.Unmarshal(raw, &syms); err != nil {
		return false, fmt.Errorf("symbols: unmarshal result: %w", err)
	}
	if len(syms) == 0 {
		return false, nil
	}
	printSymbols(w, syms, 0)
	return true, nil
}

// printSymbols recursively prints the symbol tree with indentation.
func printSymbols(w io.Writer, syms []documentSymbol, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, s := range syms {
		line := s.Range.Start.Line + 1 // convert 0-based to 1-based
		fmt.Fprintf(w, "%s%s (line %d)\n", indent, s.Name, line)
		if len(s.Children) > 0 {
			printSymbols(w, s.Children, depth+1)
		}
	}
}

// SymbolsJSON writes the raw JSON symbol result to w.
// Returns false if the result is empty.
func SymbolsJSON(w io.Writer, raw json.RawMessage) (bool, error) {
	if isNullOrEmpty(raw) {
		_, err := fmt.Fprintln(w, "[]")
		return false, err
	}
	_, err := fmt.Fprintf(w, "%s\n", raw)
	return true, err
}

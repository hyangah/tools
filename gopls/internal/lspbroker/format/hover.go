// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"encoding/json"
	"fmt"
	"io"
)

// hoverResult is a minimal struct for unmarshaling the broker's hover response.
// The full LSP Hover type may have contents as a MarkedString or MarkupContent;
// we only handle the MarkupContent form (kind + value) which gopls always uses.
type hoverResult struct {
	Contents struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"contents"`
}

// Hover writes the markdown content of a hover result to w.
// raw is the JSON from the broker (may be nil, empty, or "null").
// Returns false if the result is empty (no content to display).
func Hover(w io.Writer, raw json.RawMessage) (bool, error) {
	if isNullOrEmpty(raw) {
		return false, nil
	}
	var h hoverResult
	if err := json.Unmarshal(raw, &h); err != nil {
		return false, fmt.Errorf("hover: unmarshal result: %w", err)
	}
	if h.Contents.Value == "" {
		return false, nil
	}
	fmt.Fprintln(w, h.Contents.Value)
	return true, nil
}

// HoverJSON writes the raw JSON hover result to w.
// Returns false if the result is empty.
func HoverJSON(w io.Writer, raw json.RawMessage) (bool, error) {
	if isNullOrEmpty(raw) {
		_, err := fmt.Fprintln(w, "null")
		return false, err
	}
	_, err := fmt.Fprintf(w, "%s\n", raw)
	return true, err
}

// isNullOrEmpty returns true if the raw JSON is nil, empty, or the literal "null".
func isNullOrEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	s := string(raw)
	return s == "null"
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"golang.org/x/tools/gopls/internal/protocol"
)

// severityName maps LSP DiagnosticSeverity to a human-readable string.
func severityName(s protocol.DiagnosticSeverity) string {
	switch s {
	case protocol.SeverityError:
		return "error"
	case protocol.SeverityWarning:
		return "warning"
	case protocol.SeverityInformation:
		return "information"
	case protocol.SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// FormatDiagnostics formats diagnostics for CLI output.
//
// In text mode, each diagnostic is printed as:
//
//	file:line:col: severity: message
//
// In JSON mode, the raw JSON array is written.
//
// uri should be a file:// URI identifying the file.
func FormatDiagnostics(w io.Writer, uri string, diags []protocol.Diagnostic, jsonMode bool) {
	if len(diags) == 0 {
		return
	}

	if jsonMode {
		b, _ := json.MarshalIndent(diags, "", "  ")
		fmt.Fprintf(w, "%s\n", b)
		return
	}

	filePath := uriToPath(uri)
	relPath := makeRelative(filePath)
	for _, d := range diags {
		line := d.Range.Start.Line + 1     // 0-based → 1-based
		col := d.Range.Start.Character + 1 // 0-based → 1-based
		fmt.Fprintf(w, "%s:%d:%d: %s: %s\n",
			relPath, line, col, severityName(d.Severity), d.Message)
	}
}

// FormatProjectDiagnostics formats all project diagnostics.
//
// In text mode, diagnostics are grouped by file (sorted by URI), one per line.
// In JSON mode, the raw JSON map is written.
func FormatProjectDiagnostics(w io.Writer, all map[string][]protocol.Diagnostic, jsonMode bool) {
	if len(all) == 0 {
		return
	}

	if jsonMode {
		b, _ := json.MarshalIndent(all, "", "  ")
		fmt.Fprintf(w, "%s\n", b)
		return
	}

	// Sort URIs for deterministic output.
	uris := make([]string, 0, len(all))
	for uri := range all {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	for _, uri := range uris {
		diags := all[uri]
		FormatDiagnostics(w, uri, diags, false)
	}
}

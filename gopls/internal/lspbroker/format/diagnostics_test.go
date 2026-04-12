// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/gopls/internal/lspbroker/format"
	"golang.org/x/tools/gopls/internal/protocol"
)

func TestFormatDiagnostics_Text(t *testing.T) {
	diags := []protocol.Diagnostic{
		{
			Range: protocol.Range{
				Start: protocol.Position{Line: 9, Character: 4}, // 0-based → display as 10:5
			},
			Severity: protocol.SeverityError,
			Message:  "undeclared name: foo",
		},
		{
			Range: protocol.Range{
				Start: protocol.Position{Line: 2, Character: 0},
			},
			Severity: protocol.SeverityWarning,
			Message:  "unused import",
		},
	}

	var sb strings.Builder
	format.FormatDiagnostics(&sb, "file:///abs/path/to/file.go", diags, false)
	out := sb.String()

	if !strings.Contains(out, "10:5") {
		t.Errorf("expected line:col 10:5 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "error: undeclared name: foo") {
		t.Errorf("expected error message in output, got:\n%s", out)
	}
	if !strings.Contains(out, "3:1") {
		t.Errorf("expected line:col 3:1 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "warning: unused import") {
		t.Errorf("expected warning message in output, got:\n%s", out)
	}
}

func TestFormatDiagnostics_JSON(t *testing.T) {
	diags := []protocol.Diagnostic{
		{
			Range: protocol.Range{
				Start: protocol.Position{Line: 0, Character: 0},
			},
			Severity: protocol.SeverityError,
			Message:  "syntax error",
		},
	}

	var sb strings.Builder
	format.FormatDiagnostics(&sb, "file:///abs/path/to/file.go", diags, true)
	out := sb.String()

	if !strings.Contains(out, "syntax error") {
		t.Errorf("expected message in JSON output, got:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("expected JSON array, got:\n%s", out)
	}
}

func TestFormatProjectDiagnostics_Text(t *testing.T) {
	all := map[string][]protocol.Diagnostic{
		"file:///abs/path/to/a.go": {
			{
				Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 0}},
				Severity: protocol.SeverityError,
				Message:  "error in a",
			},
		},
		"file:///abs/path/to/b.go": {
			{
				Range:    protocol.Range{Start: protocol.Position{Line: 5, Character: 2}},
				Severity: protocol.SeverityWarning,
				Message:  "warning in b",
			},
		},
	}

	var sb strings.Builder
	format.FormatProjectDiagnostics(&sb, all, false)
	out := sb.String()

	if !strings.Contains(out, "error in a") {
		t.Errorf("expected 'error in a' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "warning in b") {
		t.Errorf("expected 'warning in b' in output, got:\n%s", out)
	}
}

func TestFormatProjectDiagnostics_JSON(t *testing.T) {
	all := map[string][]protocol.Diagnostic{
		"file:///abs/path/to/a.go": {
			{
				Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 0}},
				Severity: protocol.SeverityError,
				Message:  "error in a",
			},
		},
	}

	var sb strings.Builder
	format.FormatProjectDiagnostics(&sb, all, true)
	out := sb.String()

	if !strings.Contains(out, "error in a") {
		t.Errorf("expected message in JSON output, got:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON object, got:\n%s", out)
	}
}

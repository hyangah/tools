// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker_test

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/protocol"
)

func mkDiag(line, col uint32, sev protocol.DiagnosticSeverity, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{
		Range: protocol.Range{
			Start: protocol.Position{Line: line, Character: col},
			End:   protocol.Position{Line: line, Character: col + 1},
		},
		Severity: sev,
		Message:  msg,
	}
}

func TestDiagStore_UpdateAndForFile(t *testing.T) {
	ds := lspbroker.NewDiagStore()
	uri := "file:///tmp/foo.go"

	diags := []protocol.Diagnostic{
		mkDiag(10, 4, protocol.SeverityWarning, "unused variable"),
		mkDiag(5, 0, protocol.SeverityError, "syntax error"),
		mkDiag(20, 2, protocol.SeverityInformation, "note"),
	}

	ds.Update(uri, 1, "go", diags)

	got := ds.ForFile(uri)
	if len(got) != 3 {
		t.Fatalf("expected 3 diagnostics, got %d", len(got))
	}

	// Should be sorted by severity: Error first, then Warning, then Info.
	if got[0].Severity != protocol.SeverityError {
		t.Errorf("first diag should be Error, got %v", got[0].Severity)
	}
	if got[1].Severity != protocol.SeverityWarning {
		t.Errorf("second diag should be Warning, got %v", got[1].Severity)
	}
	if got[2].Severity != protocol.SeverityInformation {
		t.Errorf("third diag should be Information, got %v", got[2].Severity)
	}
}

func TestDiagStore_PerFileCap(t *testing.T) {
	ds := lspbroker.NewDiagStore()
	uri := "file:///tmp/foo.go"

	diags := make([]protocol.Diagnostic, 15)
	for i := range diags {
		diags[i] = mkDiag(uint32(i), 0, protocol.SeverityWarning, "warn")
	}

	ds.Update(uri, 1, "go", diags)

	got := ds.ForFile(uri)
	if len(got) != 10 {
		t.Errorf("expected cap of 10, got %d", len(got))
	}
}

func TestDiagStore_ProjectCap(t *testing.T) {
	ds := lspbroker.NewDiagStore()

	// Add 12 diagnostics to each of 5 files = 60 total.
	// ForProject should return at most 30.
	for f := 0; f < 5; f++ {
		uri := fmt.Sprintf("file:///tmp/file%d.go", f)
		diags := make([]protocol.Diagnostic, 12)
		for i := range diags {
			diags[i] = mkDiag(uint32(i), 0, protocol.SeverityWarning, "warn")
		}
		ds.Update(uri, 1, "go", diags)
	}

	all := ds.ForProject()
	total := 0
	for _, diags := range all {
		total += len(diags)
	}
	if total > 30 {
		t.Errorf("expected total ≤ 30, got %d", total)
	}
}

func TestDiagStore_Dedup(t *testing.T) {
	ds := lspbroker.NewDiagStore()
	uri := "file:///tmp/foo.go"

	d := mkDiag(5, 0, protocol.SeverityError, "syntax error")
	ds.Update(uri, 1, "go", []protocol.Diagnostic{d})

	// Get the initial timestamp.
	got1 := ds.ForFile(uri)
	if len(got1) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(got1))
	}

	// Sleep briefly so time.Now() would differ.
	time.Sleep(5 * time.Millisecond)

	// Send identical diagnostics again.
	ds.Update(uri, 2, "go", []protocol.Diagnostic{d})

	// The dedup logic doesn't expose timestamps directly, but we can verify
	// that ForFile still returns the diagnostic (content unchanged).
	got2 := ds.ForFile(uri)
	if len(got2) != 1 {
		t.Fatalf("after dedup update, expected 1 diagnostic, got %d", len(got2))
	}
	if got2[0].Message != d.Message {
		t.Errorf("expected message %q, got %q", d.Message, got2[0].Message)
	}
}

func TestDiagStore_Clear(t *testing.T) {
	ds := lspbroker.NewDiagStore()
	uri := "file:///tmp/foo.go"

	ds.Update(uri, 1, "go", []protocol.Diagnostic{
		mkDiag(5, 0, protocol.SeverityError, "err"),
	})

	ds.Clear(uri)

	got := ds.ForFile(uri)
	if got != nil {
		t.Errorf("expected nil after Clear, got %v", got)
	}
}

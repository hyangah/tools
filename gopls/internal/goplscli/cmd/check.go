// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// cliDiagnostic is the flat, printer-friendly shape of a single
// diagnostic. It collapses the LSP nesting (Or_WorkspaceDocument…,
// Or_DocumentDiagnosticReport) into the fields a CLI consumer actually
// reads. Kept internal to this package — `gopls cli` JSON output is
// authoritative documentation.
type cliDiagnostic struct {
	File     string `json:"file"`
	Line     uint32 `json:"line"`
	Column   uint32 `json:"column"`
	Severity string `json:"severity"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// runCheck implements `gopls cli check [FILE...]`. With no args it calls
// workspace/diagnostic; with file args it calls textDocument/diagnostic
// per file. Both require the pull-diagnostic profile (see
// cliProfileFor in internal/cmd/cmd.go).
func runCheck(ctx context.Context, server protocol.Server, args []string) ([]cliDiagnostic, error) {
	return runDiagnostics(ctx, server, args, nil)
}

// runVet implements `gopls cli vet [FILE...]`. Same shape as check, but
// filters diagnostics to those produced by analyzers in gopls's vet
// suite (see settings.VetAnalyzerNames). Diagnostic.Source equals the
// analyzer name, so the set-membership check is exact.
func runVet(ctx context.Context, server protocol.Server, args []string) ([]cliDiagnostic, error) {
	return runDiagnostics(ctx, server, args, settings.VetAnalyzerNames())
}

// runDiagnostics is the shared core used by check and vet. sourceFilter,
// if non-nil, restricts results to diagnostics whose Source matches.
func runDiagnostics(ctx context.Context, server protocol.Server, args []string, sourceFilter map[string]bool) ([]cliDiagnostic, error) {
	var diags []cliDiagnostic

	if len(args) == 0 {
		rep, err := server.DiagnosticWorkspace(ctx, &protocol.WorkspaceDiagnosticParams{})
		if err != nil {
			return nil, err
		}
		for _, item := range rep.Items {
			full, ok := item.Value.(protocol.WorkspaceFullDocumentDiagnosticReport)
			if !ok {
				continue // Unchanged report — server never emits, but ignore defensively.
			}
			diags = append(diags, toCLIDiagnostics(full.URI, full.Items, sourceFilter)...)
		}
	} else {
		for _, arg := range args {
			abs, err := filepath.Abs(arg)
			if err != nil {
				return nil, err
			}
			uri := protocol.URIFromPath(abs)
			rep, err := server.Diagnostic(ctx, &protocol.DocumentDiagnosticParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			})
			if err != nil {
				return nil, fmt.Errorf("%s: %v", arg, err)
			}
			full, ok := rep.Value.(protocol.RelatedFullDocumentDiagnosticReport)
			if !ok {
				continue
			}
			diags = append(diags, toCLIDiagnostics(uri, full.Items, sourceFilter)...)
		}
	}

	sort.Slice(diags, func(i, j int) bool {
		if diags[i].File != diags[j].File {
			return diags[i].File < diags[j].File
		}
		if diags[i].Line != diags[j].Line {
			return diags[i].Line < diags[j].Line
		}
		return diags[i].Column < diags[j].Column
	})
	return diags, nil
}

func toCLIDiagnostics(uri protocol.DocumentURI, items []protocol.Diagnostic, sourceFilter map[string]bool) []cliDiagnostic {
	file := uri.Path()
	out := make([]cliDiagnostic, 0, len(items))
	for _, d := range items {
		if sourceFilter != nil && !sourceFilter[d.Source] {
			continue
		}
		out = append(out, cliDiagnostic{
			File:     file,
			Line:     d.Range.Start.Line + 1,
			Column:   d.Range.Start.Character + 1,
			Severity: severityName(d.Severity),
			Source:   d.Source,
			Message:  d.Message,
		})
	}
	return out
}

func severityName(s protocol.DiagnosticSeverity) string {
	switch s {
	case protocol.SeverityError:
		return "error"
	case protocol.SeverityWarning:
		return "warning"
	case protocol.SeverityInformation:
		return "info"
	case protocol.SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

func printDiagnostics(w io.Writer, diags []cliDiagnostic) {
	for _, d := range diags {
		src := d.Source
		if src == "" {
			src = "-"
		}
		fmt.Fprintf(w, "%s:%d:%d: %s [%s]: %s\n", d.File, d.Line, d.Column, d.Severity, src, d.Message)
	}
}

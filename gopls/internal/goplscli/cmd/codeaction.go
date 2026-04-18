// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/gopls/internal/protocol"
)

// cliCodeAction is the JSON-serializable summary of a single code action,
// used by both `gopls cli codeaction` listing and `gopls cli fix`
// selection output.
type cliCodeAction struct {
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	HasEdit    bool   `json:"hasEdit"`
	HasCommand bool   `json:"hasCommand"`
	Resolvable bool   `json:"resolvable"` // action.Data non-nil, requires codeAction/resolve
}

// codeActionFlags controls selection and output for codeaction/fix.
type codeActionFlags struct {
	Kind  string // --kind KIND, filter to this CodeActionKind prefix
	Write bool   // fix only: -w overwrite files in place
	Diff  bool   // fix only: -d print unified diff
	List  bool   // fix only: -l print names of changed files
}

// parseCodeActionFlags extracts flags and leaves positional args (e.g.
// the FILE:LINE:COL argument) for the position parser.
func parseCodeActionFlags(args []string) (codeActionFlags, []string, error) {
	var flags codeActionFlags
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--kind", "-kind":
			if i+1 >= len(args) {
				return flags, nil, fmt.Errorf("%s requires a value", a)
			}
			flags.Kind = args[i+1]
			i++
		case "-w", "--write", "-write":
			flags.Write = true
		case "-d", "--diff", "-diff":
			flags.Diff = true
		case "-l", "--list", "-list":
			flags.List = true
		default:
			if strings.HasPrefix(a, "-") {
				return flags, nil, fmt.Errorf("unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	return flags, positional, nil
}

// runCodeAction implements `gopls cli codeaction FILE:LINE:COL [--kind KIND]`.
// Pulls textDocument/diagnostic for the file, filters diagnostics to
// those covering the cursor position, and calls textDocument/codeAction
// with those in Context.Diagnostics. Returns the list of actions.
func runCodeAction(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (int, error) {
	flags, positional, err := parseCodeActionFlags(args)
	if err != nil {
		return 2, err
	}
	tdpp, err := resolvePosition(ctx, server, positional)
	if err != nil {
		return 2, err
	}
	actions, err := fetchCodeActions(ctx, server, tdpp, flags.Kind)
	if err != nil {
		return 1, err
	}

	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(actions)
	}

	summaries := summarizeActions(actions)
	for _, s := range summaries {
		extra := make([]string, 0, 3)
		if s.HasEdit {
			extra = append(extra, "edit")
		}
		if s.HasCommand {
			extra = append(extra, "command")
		}
		if s.Resolvable {
			extra = append(extra, "resolvable")
		}
		tag := "-"
		if len(extra) > 0 {
			tag = strings.Join(extra, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t[%s]\n", s.Title, s.Kind, tag)
	}
	return 0, nil
}

// runFix implements `gopls cli fix FILE:LINE:COL [--kind KIND] [-w|-d|-l]`.
// Same shape as codeaction, but picks one matching action and applies
// its WorkspaceEdit. Errors if --kind matches zero actions or more than
// one (user picks by refining --kind).
func runFix(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (int, error) {
	flags, positional, err := parseCodeActionFlags(args)
	if err != nil {
		return 2, err
	}
	tdpp, err := resolvePosition(ctx, server, positional)
	if err != nil {
		return 2, err
	}
	actions, err := fetchCodeActions(ctx, server, tdpp, flags.Kind)
	if err != nil {
		return 1, err
	}
	if len(actions) == 0 {
		return 1, fmt.Errorf("no code actions available at this position (kind=%q)", flags.Kind)
	}
	if len(actions) > 1 {
		var titles []string
		for _, a := range actions {
			titles = append(titles, fmt.Sprintf("%q (%s)", a.Title, a.Kind))
		}
		return 1, fmt.Errorf("multiple actions match; refine --kind to pick one: %s", strings.Join(titles, ", "))
	}

	action := actions[0]
	if action.Edit == nil {
		if action.Data == nil {
			return 1, fmt.Errorf("action %q has no edit and no resolve data; commands are not yet supported", action.Title)
		}
		resolved, err := server.ResolveCodeAction(ctx, &action)
		if err != nil {
			return 1, fmt.Errorf("resolving action %q: %v", action.Title, err)
		}
		action = *resolved
	}
	if action.Edit == nil {
		return 1, fmt.Errorf("action %q still has no edit after resolve; commands are not yet supported", action.Title)
	}

	return applyWorkspaceEdit(jsonOutput, w, action.Edit, formatFlags{
		Write: flags.Write,
		Diff:  flags.Diff,
		List:  flags.List,
	})
}

// fetchCodeActions pulls per-position diagnostics and issues the
// CodeAction request with them in Context.Diagnostics. kind, if
// non-empty, is passed as Context.Only for server-side filtering.
func fetchCodeActions(ctx context.Context, server protocol.Server, tdpp protocol.TextDocumentPositionParams, kind string) ([]protocol.CodeAction, error) {
	uri := tdpp.TextDocument.URI
	diags, err := diagnosticsAt(ctx, server, uri, tdpp.Range.Start)
	if err != nil {
		return nil, err
	}
	params := &protocol.CodeActionParams{
		TextDocument: tdpp.TextDocument,
		Range:        tdpp.Range,
		Context: protocol.CodeActionContext{
			Diagnostics: diags,
		},
	}
	if kind != "" {
		params.Context.Only = []protocol.CodeActionKind{protocol.CodeActionKind(kind)}
	}
	return server.CodeAction(ctx, params)
}

func diagnosticsAt(ctx context.Context, server protocol.Server, uri protocol.DocumentURI, pos protocol.Position) ([]protocol.Diagnostic, error) {
	rep, err := server.Diagnostic(ctx, &protocol.DocumentDiagnosticParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return nil, err
	}
	full, ok := rep.Value.(protocol.RelatedFullDocumentDiagnosticReport)
	if !ok {
		return nil, nil
	}
	var out []protocol.Diagnostic
	for _, d := range full.Items {
		if d.Range.Contains(pos) {
			out = append(out, d)
		}
	}
	return out, nil
}

func summarizeActions(actions []protocol.CodeAction) []cliCodeAction {
	out := make([]cliCodeAction, 0, len(actions))
	for _, a := range actions {
		out = append(out, cliCodeAction{
			Title:      a.Title,
			Kind:       string(a.Kind),
			HasEdit:    a.Edit != nil,
			HasCommand: a.Command != nil,
			Resolvable: a.Edit == nil && a.Data != nil,
		})
	}
	return out
}

// applyWorkspaceEdit applies a WorkspaceEdit's DocumentChanges using
// the same text-edit pipeline that format/imports use, iterating over
// every TextDocumentEdit in the edit.
func applyWorkspaceEdit(jsonOutput bool, w io.Writer, edit *protocol.WorkspaceEdit, flags formatFlags) (int, error) {
	var results []cliFileEdit
	for _, c := range edit.DocumentChanges {
		if c.TextDocumentEdit == nil {
			continue // CreateFile/DeleteFile/RenameFile not yet supported
		}
		uri := c.TextDocumentEdit.TextDocument.URI
		path := uri.Path()
		old, err := os.ReadFile(path)
		if err != nil {
			return 1, err
		}
		mapper := protocol.NewMapper(uri, old)
		newContent, diffEdits, err := protocol.ApplyEdits(mapper, protocol.AsTextEdits(c.TextDocumentEdit.Edits))
		if err != nil {
			return 1, fmt.Errorf("%s: %v", path, err)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return 1, err
		}
		changed := !bytes.Equal(old, newContent)

		if jsonOutput {
			results = append(results, cliFileEdit{
				File:       abs,
				Changed:    changed,
				NewContent: string(newContent),
			})
			continue
		}
		if err := emitFormatOutput(w, flags, abs, old, newContent, diffEdits, changed); err != nil {
			return 1, err
		}
	}
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return 1, err
		}
	}
	return 0, nil
}

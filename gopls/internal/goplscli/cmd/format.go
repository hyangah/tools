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
	"golang.org/x/tools/internal/diff"
)

// formatFlags controls how textDocument/formatting edits are presented
// to the user. Parsed from args in runFormat; shared with runImports
// since both subcommands return edits.
type formatFlags struct {
	Write    bool // -w: overwrite the file in place
	Preserve bool // with -write, make copies of original files
	Diff     bool // -d: print a unified diff
	List     bool // -l: print the names of files that would change
}

// parseFormatFlags splits args into edit-mode flags and positional file
// arguments. Only long form is supported to keep the parser simple
// (-write, -diff, -list, -preserve, or their double-dash equivalents); the flags
// are orthogonal and any combination is valid.
func parseFormatFlags(args []string) (formatFlags, []string, error) {
	var flags formatFlags
	var positional []string
	for _, a := range args {
		switch a {
		case "-w", "--write", "-write":
			flags.Write = true
		case "-d", "--diff", "-diff":
			flags.Diff = true
		case "-l", "--list", "-list":
			flags.List = true
		case "-preserve", "--preserve":
			flags.Preserve = true
		default:
			if strings.HasPrefix(a, "-") {
				return flags, nil, fmt.Errorf("unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	return flags, positional, nil
}

// cliFileEdit is the JSON-serializable shape for a single file's edits
// after formatting or organizing imports.
type cliFileEdit struct {
	File       string `json:"file"`
	Changed    bool   `json:"changed"`
	NewContent string `json:"newContent,omitempty"`
}

// runFormat implements `gopls cli format [flags] FILE...`. It handles
// its own output because the edit-mode flags (-w/-d/-l) control writes
// and per-file rendering that don't fit the standard result→printer
// pipeline. Returns (exitCode, error).
func runFormat(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (int, error) {
	return runFormatting(ctx, server, jsonOutput, args, w, formattingCall)
}

// runImports implements `gopls cli imports [flags] FILE...`. Same shape
// as runFormat but requests the SourceOrganizeImports code action instead.
func runImports(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (int, error) {
	return runFormatting(ctx, server, jsonOutput, args, w, organizeImportsCall)
}

// editCall requests edits for a single file.
type editCall func(ctx context.Context, server protocol.Server, uri protocol.DocumentURI) ([]protocol.TextEdit, error)

func formattingCall(ctx context.Context, server protocol.Server, uri protocol.DocumentURI) ([]protocol.TextEdit, error) {
	return server.Formatting(ctx, &protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
}

func organizeImportsCall(ctx context.Context, server protocol.Server, uri protocol.DocumentURI) ([]protocol.TextEdit, error) {
	actions, err := server.CodeAction(ctx, &protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Context: protocol.CodeActionContext{
			Only: []protocol.CodeActionKind{protocol.SourceOrganizeImports},
		},
	})
	if err != nil {
		return nil, err
	}
	for _, a := range actions {
		if a.Kind != protocol.SourceOrganizeImports || a.Edit == nil {
			continue
		}
		for _, c := range a.Edit.DocumentChanges {
			if c.TextDocumentEdit == nil || c.TextDocumentEdit.TextDocument.URI != uri {
				continue
			}
			return protocol.AsTextEdits(c.TextDocumentEdit.Edits), nil
		}
	}
	return nil, nil
}

func runFormatting(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer, call editCall) (int, error) {
	flags, files, err := parseFormatFlags(args)
	if err != nil {
		return 2, err
	}
	if len(files) == 0 {
		return 2, fmt.Errorf("usage: FILE...")
	}

	var results []cliFileEdit
	for _, arg := range files {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return 1, err
		}
		uri := protocol.URIFromPath(abs)
		edits, err := call(ctx, server, uri)
		if err != nil {
			return 1, fmt.Errorf("%s: %v", arg, err)
		}

		old, err := os.ReadFile(abs)
		if err != nil {
			return 1, err
		}
		mapper := protocol.NewMapper(uri, old)
		newContent, diffEdits, err := protocol.ApplyEdits(mapper, edits)
		if err != nil {
			return 1, fmt.Errorf("%s: %v", arg, err)
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

func emitFormatOutput(w io.Writer, flags formatFlags, filename string, old, new []byte, edits []diff.Edit, changed bool) error {
	if flags.List && changed {
		fmt.Fprintln(w, filename)
	}
	if flags.Write && changed {
		if flags.Preserve {
			if err := os.WriteFile(filename+".orig", old, 0o666); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filename, new, 0o666); err != nil {
			return err
		}
	}
	if flags.Diff && changed {
		unified, err := diff.ToUnified(filename+".orig", filename, string(old), edits, diff.DefaultContextLines)
		if err != nil {
			return err
		}
		fmt.Fprint(w, unified)
	}
	// Default: print edited content to stdout when no flags chosen.
	if !flags.List && !flags.Write && !flags.Diff {
		w.Write(new)
	}
	return nil
}

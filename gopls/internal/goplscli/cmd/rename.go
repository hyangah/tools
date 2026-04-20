// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/tools/gopls/internal/protocol"
)

// renameFlags controls how textDocument/rename edits are applied or presented.
type renameFlags struct {
	Write  bool // -w: overwrite files in place
	Diff   bool // -d: print a unified diff
	List   bool // -l: print names of changed files
	DryRun bool // --dry-run: print terse change summary without applying
}

// parseRenameFlags splits args into rename flags, positional args (for
// position resolution via resolvePosition), and the --to value.
// The --in flag is a positional-resolution flag consumed by resolvePosition;
// it is passed through in the returned positional slice.
func parseRenameFlags(args []string) (renameFlags, []string, string, error) {
	var flags renameFlags
	var positional []string
	var newName string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--to", "-to":
			if i+1 >= len(args) {
				return flags, nil, "", fmt.Errorf("%s requires a value", a)
			}
			newName = args[i+1]
			i++
		case "--in", "-in":
			// --in FILE is consumed by resolvePosition; pass it through.
			if i+1 >= len(args) {
				return flags, nil, "", fmt.Errorf("%s requires a value", a)
			}
			positional = append(positional, a, args[i+1])
			i++
		case "-w", "--write", "-write":
			flags.Write = true
		case "-d", "--diff", "-diff":
			flags.Diff = true
		case "-l", "--list", "-list":
			flags.List = true
		case "--dry-run", "-dry-run":
			flags.DryRun = true
		default:
			if strings.HasPrefix(a, "-") {
				return flags, nil, "", fmt.Errorf("unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	return flags, positional, newName, nil
}

// runRename implements `gopls cli rename [flags] FILE:LINE:COL --to NEWNAME`
// or `gopls cli rename [flags] SYMBOL --in FILE --to NEWNAME`.
//
// Without --dry-run, the WorkspaceEdit is applied using the same machinery as
// cli fix (applyWorkspaceEdit), which respects -w/-d/-l. Without any flag, it
// prints the full new content of each changed file to stdout.
//
// With --dry-run, a terse summary of what would change is printed (file path +
// line/col ranges + new text), and no files are modified.
func runRename(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (int, error) {
	flags, positional, newName, err := parseRenameFlags(args)
	if err != nil {
		return 2, err
	}
	if newName == "" {
		return 2, fmt.Errorf("usage: gopls cli rename [flags] FILE:LINE:COL --to NEWNAME")
	}

	tdpp, err := resolvePosition(ctx, server, positional)
	if err != nil {
		return 2, err
	}

	edit, err := server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: tdpp,
		NewName:                    newName,
	})
	if err != nil {
		return 1, err
	}

	if flags.DryRun {
		printRenameEdit(w, edit)
		return 0, nil
	}

	return applyWorkspaceEdit(jsonOutput, w, edit, formatFlags{
		Write: flags.Write,
		Diff:  flags.Diff,
		List:  flags.List,
	})
}

// printRenameEdit prints a terse summary of a WorkspaceEdit returned by
// textDocument/rename. Used by --dry-run to show what would change without
// modifying any files. gopls always populates DocumentChanges (the modern
// field), regardless of client capability — so we read from there rather than
// the legacy Changes map.
func printRenameEdit(w io.Writer, edit *protocol.WorkspaceEdit) {
	for _, c := range edit.DocumentChanges {
		if c.TextDocumentEdit == nil {
			continue
		}
		uri := c.TextDocumentEdit.TextDocument.URI
		fmt.Fprintf(w, "%s:\n", uri.Path())
		for _, e := range protocol.AsTextEdits(c.TextDocumentEdit.Edits) {
			fmt.Fprintf(w, "  %d:%d-%d:%d → %q\n",
				e.Range.Start.Line+1, e.Range.Start.Character+1,
				e.Range.End.Line+1, e.Range.End.Character+1,
				e.NewText)
		}
	}
	for uri, edits := range edit.Changes {
		fmt.Fprintf(w, "%s:\n", uri.Path())
		for _, e := range edits {
			fmt.Fprintf(w, "  %d:%d-%d:%d → %q\n",
				e.Range.Start.Line+1, e.Range.Start.Character+1,
				e.Range.End.Line+1, e.Range.End.Character+1,
				e.NewText)
		}
	}
}

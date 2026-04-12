// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/gopls/internal/protocol"
)

// ApplyWorkspaceEdit applies the edits in we to the files on disk.
// It returns a [RenameResult] summarising the changes. If dryRun is
// true the files are not modified and Applied in the result is false.
//
// rootDir is the project root directory. All edited file paths must
// resolve (after symlink evaluation) to paths inside rootDir; if any
// path escapes the root the entire edit is rejected. This prevents a
// compromised language server from writing to arbitrary files.
//
// The function processes DocumentChanges (modern) if present, falling
// back to the legacy Changes map. Only TextDocumentEdit entries are
// applied; CreateFile, RenameFile, and DeleteFile operations are
// silently skipped (they are not produced by textDocument/rename for
// simple identifier renames).
//
// Edits within a single file are applied in reverse position order so
// that earlier edits do not invalidate the offsets of later ones.
func ApplyWorkspaceEdit(we *protocol.WorkspaceEdit, rootDir string, dryRun bool) (*RenameResult, error) {
	if we == nil {
		return &RenameResult{Applied: !dryRun}, nil
	}

	// Collect per-file edit lists. Use the modern DocumentChanges form if
	// available; fall back to the legacy Changes map.
	type fileEdits struct {
		path  string
		edits []protocol.TextEdit
	}
	var files []fileEdits

	if len(we.DocumentChanges) > 0 {
		// Modern form: []DocumentChange, each may be a TextDocumentEdit.
		for _, dc := range we.DocumentChanges {
			if dc.TextDocumentEdit == nil {
				// Skip CreateFile/RenameFile/DeleteFile for now.
				continue
			}
			tde := dc.TextDocumentEdit
			path := tde.TextDocument.URI.Path()
			var edits []protocol.TextEdit
			for _, e := range tde.Edits {
				te, err := extractTextEdit(e)
				if err != nil {
					return nil, fmt.Errorf("editapply: %s: %w", path, err)
				}
				edits = append(edits, te)
			}
			if len(edits) > 0 {
				files = append(files, fileEdits{path: path, edits: edits})
			}
		}
	} else if len(we.Changes) > 0 {
		// Legacy form: map[DocumentURI][]TextEdit.
		for uri, edits := range we.Changes {
			path := uri.Path()
			files = append(files, fileEdits{path: path, edits: edits})
		}
	}

	// Sort files by path for deterministic output.
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	// Resolve the root directory for containment checks.
	absRoot, err := filepath.EvalSymlinks(filepath.Clean(rootDir))
	if err != nil {
		return nil, fmt.Errorf("editapply: resolve root %s: %w", rootDir, err)
	}
	rootPrefix := absRoot + string(filepath.Separator)

	// Pre-flight: verify all files exist and are inside the project root.
	// Use Lstat to detect symlinks, then EvalSymlinks to get the real path.
	for i, f := range files {
		realPath, err := filepath.EvalSymlinks(f.path)
		if err != nil {
			return nil, fmt.Errorf("editapply: resolve path %s: %w", f.path, err)
		}
		if realPath != absRoot && !strings.HasPrefix(realPath, rootPrefix) {
			return nil, fmt.Errorf("editapply: path %s resolves to %s which is outside project root %s", f.path, realPath, rootDir)
		}
		// Use the resolved path for all subsequent operations.
		files[i].path = realPath
	}

	var changes []FileChange
	for _, f := range files {
		fi, err := os.Stat(f.path)
		if err != nil {
			return nil, fmt.Errorf("editapply: stat %s: %w", f.path, err)
		}
		content, err := os.ReadFile(f.path)
		if err != nil {
			return nil, fmt.Errorf("editapply: read %s: %w", f.path, err)
		}
		updated, err := applyTextEditsToContent(content, f.edits)
		if err != nil {
			return nil, fmt.Errorf("editapply: apply edits to %s: %w", f.path, err)
		}
		if !dryRun {
			if err := atomicWriteFile(f.path, updated, fi.Mode().Perm()); err != nil {
				return nil, fmt.Errorf("editapply: write %s: %w", f.path, err)
			}
		}
		changes = append(changes, FileChange{Path: f.path, Edits: len(f.edits)})
	}

	return &RenameResult{Changes: changes, Applied: !dryRun}, nil
}

// applyTextEditsToContent applies edits to content in reverse position
// order and returns the updated bytes.
func applyTextEditsToContent(content []byte, edits []protocol.TextEdit) ([]byte, error) {
	if len(edits) == 0 {
		return content, nil
	}

	// Split into lines (preserve \r\n vs \n).
	lines := splitLines(content)

	// Sort edits in reverse order (last line first; within same line, last char first).
	sorted := make([]protocol.TextEdit, len(edits))
	copy(sorted, edits)
	sort.Slice(sorted, func(i, j int) bool {
		si := sorted[i].Range.Start
		sj := sorted[j].Range.Start
		if si.Line != sj.Line {
			return si.Line > sj.Line
		}
		return si.Character > sj.Character
	})

	for _, e := range sorted {
		startLine := int(e.Range.Start.Line)
		startChar := int(e.Range.Start.Character)
		endLine := int(e.Range.End.Line)
		endChar := int(e.Range.End.Character)

		if startLine >= len(lines) || endLine >= len(lines) {
			return nil, fmt.Errorf("edit range [%d:%d-%d:%d] out of bounds (file has %d lines)",
				startLine, startChar, endLine, endChar, len(lines))
		}

		// The text to replace spans from (startLine, startChar) to (endLine, endChar).
		// Reconstruct the full span as a single byte slice, replace the range, then
		// re-split into lines.
		var combined []byte
		for i := startLine; i <= endLine; i++ {
			combined = append(combined, lines[i]...)
		}

		// Within combined, the range is [startChar, len(combined)-len(lines[endLine])+endChar).
		prefixLen := startChar
		suffixOffset := 0
		for i := startLine; i < endLine; i++ {
			suffixOffset += len(lines[i])
		}
		suffixOffset += endChar

		if prefixLen > suffixOffset || suffixOffset > len(combined) {
			return nil, fmt.Errorf("edit range [%d:%d-%d:%d] invalid: combined length %d",
				startLine, startChar, endLine, endChar, len(combined))
		}

		newCombined := append(combined[:prefixLen], append([]byte(e.NewText), combined[suffixOffset:]...)...)

		// Re-split the replaced span back into lines.
		newLines := splitLines(newCombined)
		if len(newLines) == 0 {
			newLines = [][]byte{{}}
		}

		// Replace lines[startLine..endLine] with newLines.
		replacement := make([][]byte, 0, len(lines)+len(newLines)-(endLine-startLine+1))
		replacement = append(replacement, lines[:startLine]...)
		replacement = append(replacement, newLines...)
		replacement = append(replacement, lines[endLine+1:]...)
		lines = replacement
	}

	var out []byte
	for _, l := range lines {
		out = append(out, l...)
	}
	return out, nil
}

// splitLines splits content into lines, each including its trailing
// newline character(s). The last element may not have a trailing newline.
func splitLines(content []byte) [][]byte {
	if len(content) == 0 {
		return [][]byte{{}}
	}
	var lines [][]byte
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

// atomicWriteFile writes data to path atomically by writing to a temp
// file in the same directory and then renaming over the target.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".lspbroker-rename-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// extractTextEdit extracts a [protocol.TextEdit] from the union type
// [protocol.Or_TextDocumentEdit_edits_Elem].
func extractTextEdit(e protocol.Or_TextDocumentEdit_edits_Elem) (protocol.TextEdit, error) {
	switch v := e.Value.(type) {
	case protocol.TextEdit:
		return v, nil
	case protocol.AnnotatedTextEdit:
		return v.TextEdit, nil
	default:
		return protocol.TextEdit{}, fmt.Errorf("unsupported edit type %T", e.Value)
	}
}

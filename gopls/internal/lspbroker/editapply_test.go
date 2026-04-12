// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/gopls/internal/protocol"
)

func TestApplyTextEditsToContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		edits   []protocol.TextEdit
		want    string
	}{
		{
			name:    "no edits",
			content: "hello world\n",
			edits:   nil,
			want:    "hello world\n",
		},
		{
			name:    "single word replacement same line",
			content: "hello world\n",
			edits: []protocol.TextEdit{
				{
					Range: protocol.Range{
						Start: protocol.Position{Line: 0, Character: 6},
						End:   protocol.Position{Line: 0, Character: 11},
					},
					NewText: "Go",
				},
			},
			want: "hello Go\n",
		},
		{
			name:    "multiple edits on same line applied in reverse order",
			content: "func Foo(x int) int { return x }\n",
			edits: []protocol.TextEdit{
				{
					Range: protocol.Range{
						Start: protocol.Position{Line: 0, Character: 5},
						End:   protocol.Position{Line: 0, Character: 8},
					},
					NewText: "Bar",
				},
			},
			want: "func Bar(x int) int { return x }\n",
		},
		{
			name:    "multi-line content edit within line",
			content: "line1\nline2\nline3\n",
			edits: []protocol.TextEdit{
				{
					Range: protocol.Range{
						Start: protocol.Position{Line: 1, Character: 0},
						End:   protocol.Position{Line: 1, Character: 5},
					},
					NewText: "REPLACED",
				},
			},
			want: "line1\nREPLACED\nline3\n",
		},
		{
			name:    "two edits on different lines",
			content: "foo\nbar\nbaz\n",
			edits: []protocol.TextEdit{
				{
					Range:   protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 3}},
					NewText: "FOO",
				},
				{
					Range:   protocol.Range{Start: protocol.Position{Line: 2, Character: 0}, End: protocol.Position{Line: 2, Character: 3}},
					NewText: "BAZ",
				},
			},
			want: "FOO\nbar\nBAZ\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyTextEditsToContent([]byte(tt.content), tt.edits)
			if err != nil {
				t.Fatalf("applyTextEditsToContent: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestApplyWorkspaceEdit_DryRun(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	file := filepath.Join(dir, "foo.go")
	original := "package foo\n\nfunc Greeting(name string) string { return name }\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	uri := protocol.URIFromPath(file)
	we := &protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentURI][]protocol.TextEdit{
			uri: {
				{
					Range: protocol.Range{
						Start: protocol.Position{Line: 2, Character: 5},
						End:   protocol.Position{Line: 2, Character: 13},
					},
					NewText: "Hello",
				},
			},
		},
	}

	result, err := ApplyWorkspaceEdit(we, dir, true /* dryRun */)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit dry run: %v", err)
	}
	if result.Applied {
		t.Error("dry run: Applied should be false")
	}
	if len(result.Changes) != 1 {
		t.Fatalf("dry run: got %d changes, want 1", len(result.Changes))
	}
	// File on disk should be unchanged.
	data, _ := os.ReadFile(file)
	if string(data) != original {
		t.Errorf("dry run modified the file; got %q", string(data))
	}
}

func TestApplyWorkspaceEdit_Applied(t *testing.T) {
	dir := t.TempDir()
	// Resolve symlinks so comparisons match (macOS /var → /private/var).
	dir, _ = filepath.EvalSymlinks(dir)
	file := filepath.Join(dir, "foo.go")
	original := "package foo\n\nfunc Greeting(name string) string { return name }\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	uri := protocol.URIFromPath(file)
	we := &protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentURI][]protocol.TextEdit{
			uri: {
				{
					Range: protocol.Range{
						Start: protocol.Position{Line: 2, Character: 5},
						End:   protocol.Position{Line: 2, Character: 13},
					},
					NewText: "Hello",
				},
			},
		},
	}

	result, err := ApplyWorkspaceEdit(we, dir, false /* dryRun */)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if !result.Applied {
		t.Error("Applied should be true")
	}
	if len(result.Changes) != 1 || result.Changes[0].Path != file {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
	want := "package foo\n\nfunc Hello(name string) string { return name }\n"
	data, _ := os.ReadFile(file)
	if string(data) != want {
		t.Errorf("file content: got %q, want %q", string(data), want)
	}
}

func TestApplyWorkspaceEdit_Nil(t *testing.T) {
	result, err := ApplyWorkspaceEdit(nil, t.TempDir(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
}

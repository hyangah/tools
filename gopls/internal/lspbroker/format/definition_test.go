// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"bytes"
	"testing"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

func TestDefinition(t *testing.T) {
	loc := func(uri string, startLine, startChar int) lspbroker.Location {
		return lspbroker.Location{
			URI: uri,
			Range: lspbroker.Range{
				Start: lspbroker.Position{Line: startLine, Character: startChar},
				End:   lspbroker.Position{Line: startLine, Character: startChar + 5},
			},
		}
	}

	tests := []struct {
		name       string
		inputFile  string
		inputLine  int
		inputChar  int
		locs       []lspbroker.Location
		wantOutput string
	}{
		{
			name:       "empty results",
			inputFile:  "/tmp/foo.go",
			inputLine:  10,
			inputChar:  5,
			locs:       nil,
			wantOutput: "",
		},
		{
			name:      "single result",
			inputFile: "/tmp/foo.go",
			inputLine: 10,
			inputChar: 5,
			locs:      []lspbroker.Location{loc("file:///tmp/bar.go", 99, 4)},
			// 0-based → 1-based: line 99 → 100, char 4 → 5
			wantOutput: "/tmp/foo.go:10:5 → /tmp/bar.go:100:5\n",
		},
		{
			name:       "is itself a definition",
			inputFile:  "/tmp/foo.go",
			inputLine:  10,
			inputChar:  5,
			locs:       []lspbroker.Location{loc("file:///tmp/foo.go", 9, 4)},
			wantOutput: "/tmp/foo.go:10:5 is itself a definition\n",
		},
		{
			name:      "multiple results",
			inputFile: "/tmp/foo.go",
			inputLine: 10,
			inputChar: 5,
			locs: []lspbroker.Location{
				loc("file:///tmp/bar.go", 99, 4),
				loc("file:///tmp/baz.go", 199, 0),
			},
			wantOutput: "/tmp/bar.go:100:5\n/tmp/baz.go:200:1\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			Definition(&buf, tc.inputFile, tc.inputLine, tc.inputChar, tc.locs)
			got := buf.String()
			if got != tc.wantOutput {
				t.Errorf("Definition output:\ngot:  %q\nwant: %q", got, tc.wantOutput)
			}
		})
	}
}

func TestURIToPath(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"file:///tmp/foo.go", "/tmp/foo.go"},
		{"file:///home/user/proj/main.go", "/home/user/proj/main.go"},
		{"/not/a/uri", "/not/a/uri"},
		{"file:///path%20with%20spaces/x.go", "/path with spaces/x.go"},
	}
	for _, tc := range tests {
		got := uriToPath(tc.uri)
		if got != tc.want {
			t.Errorf("uriToPath(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

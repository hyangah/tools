// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGoModRoot(t *testing.T) {
	// Build a temp tree:
	//   root/
	//     go.mod
	//     a/
	//       b/
	//         file.go
	//     workspace/        (has go.work, should win over inner go.mod)
	//       sub/
	//         go.mod
	//         pkg/
	//           x.go

	base := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Simple go.mod tree.
	write(filepath.Join(base, "root", "go.mod"), "module example.com/root\n")
	write(filepath.Join(base, "root", "a", "b", "file.go"), "package b\n")

	// go.work tree: go.work at workspace/, go.mod at workspace/sub/.
	write(filepath.Join(base, "workspace", "go.work"), "go 1.22\n")
	write(filepath.Join(base, "workspace", "sub", "go.mod"), "module example.com/sub\n")
	write(filepath.Join(base, "workspace", "sub", "pkg", "x.go"), "package pkg\n")

	tests := []struct {
		name string
		dir  string
		want string // relative to base, or "" for "not found"
	}{
		{
			name: "direct match go.mod",
			dir:  filepath.Join(base, "root"),
			want: filepath.Join(base, "root"),
		},
		{
			name: "nested under go.mod",
			dir:  filepath.Join(base, "root", "a", "b"),
			want: filepath.Join(base, "root"),
		},
		{
			name: "go.work wins over nested go.mod",
			dir:  filepath.Join(base, "workspace", "sub", "pkg"),
			want: filepath.Join(base, "workspace"),
		},
		{
			name: "no module root",
			dir:  base,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FindGoModRoot(tc.dir)
			if got != tc.want {
				t.Errorf("FindGoModRoot(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

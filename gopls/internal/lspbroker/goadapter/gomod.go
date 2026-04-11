// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter

import (
	"os"
	"path/filepath"
)

// FindGoModRoot walks up the directory tree starting at dir, looking for a
// go.work or go.mod file. It returns the directory that contains the file,
// or an empty string if no such file is found before reaching the filesystem
// root.
//
// go.work takes priority over go.mod: if any ancestor directory contains
// go.work, that directory is returned regardless of any go.mod found at
// a closer level. This mirrors how the Go toolchain selects workspace roots.
func FindGoModRoot(dir string) string {
	// First pass: walk up looking for go.work. go.work takes precedence
	// because a workspace file at any ancestor governs all modules below it.
	for d := dir; ; {
		if fileExists(filepath.Join(d, "go.work")) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	// Second pass: walk up looking for the nearest go.mod.
	for d := dir; ; {
		if fileExists(filepath.Join(d, "go.mod")) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// fileExists reports whether path refers to an existing regular file or
// directory (not a broken symlink or permission error).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

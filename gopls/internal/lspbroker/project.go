// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"os"
	"path/filepath"
	"sync"
)

// projectRootCache caches FindProjectRoot results. The key is the absolute
// directory path; the value is the found root string.
var projectRootCache sync.Map

// resetProjectRootCache clears the package-level project root cache.
// It is intended for use in tests to avoid cross-contamination between subtests.
func resetProjectRootCache() {
	projectRootCache.Range(func(k, v any) bool {
		projectRootCache.Delete(k)
		return true
	})
}

// FindProjectRoot walks up the directory tree from filePath looking for
// project root markers. It returns the first directory containing one of
// these sentinels (in priority order):
//  1. .lsp.json
//  2. go.work
//  3. go.mod
//  4. .git/ (directory)
//  5. .hg/ (directory)
//
// Walking stops at $HOME — never walk beyond the user's home directory.
// Returns ("", ErrProjectNotFound) if no sentinel is found.
//
// Results are cached per absolute directory path in a package-level sync.Map.
func FindProjectRoot(filePath string) (string, error) {
	dir := filepath.Dir(filePath)

	// Check cache for this starting directory.
	if v, ok := projectRootCache.Load(dir); ok {
		root := v.(string)
		if root == "" {
			return "", ErrProjectNotFound
		}
		return root, nil
	}

	home, _ := os.UserHomeDir()
	root := findProjectRootUncached(dir, home)

	// Cache the result: empty string means not found.
	projectRootCache.Store(dir, root)

	if root == "" {
		return "", ErrProjectNotFound
	}
	return root, nil
}

// findProjectRootUncached performs the actual multi-pass walk without caching.
// home is the user's home directory; walking stops there.
// Returns "" if no sentinel is found.
func findProjectRootUncached(startDir, home string) string {
	// Pass 1: .lsp.json (highest priority)
	if d := walkUpFor(startDir, home, func(dir string) bool {
		return fileExistsAny(filepath.Join(dir, ".lsp.json"))
	}); d != "" {
		return d
	}

	// Pass 2: go.work
	if d := walkUpFor(startDir, home, func(dir string) bool {
		return fileExistsAny(filepath.Join(dir, "go.work"))
	}); d != "" {
		return d
	}

	// Pass 3: go.mod
	if d := walkUpFor(startDir, home, func(dir string) bool {
		return fileExistsAny(filepath.Join(dir, "go.mod"))
	}); d != "" {
		return d
	}

	// Pass 4: .git/ (must be a directory)
	if d := walkUpFor(startDir, home, func(dir string) bool {
		return isDirAt(filepath.Join(dir, ".git"))
	}); d != "" {
		return d
	}

	// Pass 5: .hg/ (must be a directory)
	if d := walkUpFor(startDir, home, func(dir string) bool {
		return isDirAt(filepath.Join(dir, ".hg"))
	}); d != "" {
		return d
	}

	return ""
}

// walkUpFor walks up the directory tree from startDir, stopping at home
// (inclusive — home itself is checked). It returns the first directory
// for which match returns true, or "" if none is found.
func walkUpFor(startDir, home string, match func(string) bool) string {
	for d := startDir; ; {
		if match(d) {
			return d
		}
		// Stop after checking home; don't go beyond it.
		if d == home {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			// Reached filesystem root.
			break
		}
		d = parent
	}
	return ""
}

// fileExistsAny reports whether path exists (file or directory).
func fileExistsAny(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isDirAt reports whether path exists and is a directory.
func isDirAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

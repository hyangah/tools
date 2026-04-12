// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveFile converts a user-supplied file argument to an absolute path
// and verifies that the file exists. Relative paths are resolved against
// the current working directory. Symlinks are not resolved — the LSP
// server sees the path as supplied; see designs/06-cli-surface.md for
// the rationale.
func resolveFile(arg string) (string, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", arg, err)
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s: file not found", arg)
		}
		return "", fmt.Errorf("%s: %w", arg, err)
	}
	return abs, nil
}

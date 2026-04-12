// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"encoding/json"
	"fmt"
	"io"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

// Rename writes the result of a gopls lspcli rename invocation to w.
//
// If result.Applied is true, the changes were written to disk; otherwise
// this is a dry-run preview. Each changed file is printed on its own line
// with the number of edits applied.
//
// Applied output format:
//
//	renamed: internal/foo/bar.go (3 edits)
//	renamed: internal/foo/baz.go (1 edit)
//
// Dry-run output format:
//
//	would rename: internal/foo/bar.go (3 edits)
//	would rename: internal/foo/baz.go (1 edit)
//
// An empty result prints nothing.
func Rename(w io.Writer, result *lspbroker.RenameResult) {
	if result == nil || len(result.Changes) == 0 {
		return
	}
	verb := "renamed"
	if !result.Applied {
		verb = "would rename"
	}
	for _, fc := range result.Changes {
		suffix := "edit"
		if fc.Edits != 1 {
			suffix = "edits"
		}
		fmt.Fprintf(w, "%s: %s (%d %s)\n", verb, makeRelative(fc.Path), fc.Edits, suffix)
	}
}

// RenameJSON writes result as a compact JSON object to w. It is used when
// the --json flag is set.
func RenameJSON(w io.Writer, result *lspbroker.RenameResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"fmt"
	"io"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

// References writes the result of a gopls lspcli refs or impl invocation to w.
// Each location is printed as "file:line:col" on its own line.
func References(w io.Writer, locs []lspbroker.Location) {
	for _, loc := range locs {
		destFile := uriToPath(loc.URI)
		destLine := loc.Range.Start.Line + 1 // convert 0-based to 1-based
		destChar := loc.Range.Start.Character + 1
		fmt.Fprintf(w, "%s:%d:%d\n", makeRelative(destFile), destLine, destChar)
	}
}

// ReferencesJSON writes the raw JSON representation of the broker
// Location slice to w. It is used when the --json flag is set.
func ReferencesJSON(w io.Writer, locs []lspbroker.Location) error {
	return DefinitionJSON(w, locs)
}

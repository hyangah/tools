// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package format

import (
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

// Definition writes the result of a gopls lspcli def invocation to w.
//
// inputFile is the absolute path of the source file the user queried.
// inputLine and inputChar are the 1-based cursor position.
// locs is the slice of definition locations returned by the broker; it
// may be nil or empty (the caller should then exit with code 1).
//
// Output format (single result):
//
//	cmd/main.go:42:10 → internal/foo/bar.go:100:5
//
// Output format (multiple results, one per line, no arrow prefix):
//
//	internal/foo/bar.go:100:5
//	internal/foo/baz.go:200:1
//
// If the single result location equals the input location, the message
// "FILE:LINE:COL is itself a definition" is printed instead.
func Definition(w io.Writer, inputFile string, inputLine, inputChar int, locs []lspbroker.Location) {
	if len(locs) == 0 {
		return
	}

	if len(locs) == 1 {
		loc := locs[0]
		destFile := uriToPath(loc.URI)
		destLine := loc.Range.Start.Line + 1 // convert 0-based to 1-based
		destChar := loc.Range.Start.Character + 1

		inputRel := makeRelative(inputFile)
		destRel := makeRelative(destFile)

		// Name-based query (no input position): show destination only.
		if inputLine == 0 {
			fmt.Fprintf(w, "%s:%d:%d\n", destRel, destLine, destChar)
			return
		}

		// Detect "is itself a definition": same file and position.
		if destFile == inputFile && destLine == inputLine && destChar == inputChar {
			fmt.Fprintf(w, "%s:%d:%d is itself a definition\n",
				inputRel, inputLine, inputChar)
			return
		}

		fmt.Fprintf(w, "%s:%d:%d → %s:%d:%d\n",
			inputRel, inputLine, inputChar,
			destRel, destLine, destChar)
		return
	}

	// Multiple results: print one per line.
	for _, loc := range locs {
		destFile := uriToPath(loc.URI)
		destLine := loc.Range.Start.Line + 1
		destChar := loc.Range.Start.Character + 1
		fmt.Fprintf(w, "%s:%d:%d\n", makeRelative(destFile), destLine, destChar)
	}
}

// DefinitionJSON writes the raw JSON representation of the broker
// Location slice to w. It is used when the --json flag is set.
func DefinitionJSON(w io.Writer, locs []lspbroker.Location) error {
	if len(locs) == 0 {
		_, err := fmt.Fprintln(w, "[]")
		return err
	}
	// Produce a compact JSON array of location objects.
	fmt.Fprint(w, "[\n")
	for i, loc := range locs {
		comma := ","
		if i == len(locs)-1 {
			comma = ""
		}
		fmt.Fprintf(w, "  {\"uri\": %q, \"range\": {\"start\": {\"line\": %d, \"character\": %d}, \"end\": {\"line\": %d, \"character\": %d}}}%s\n",
			loc.URI,
			loc.Range.Start.Line, loc.Range.Start.Character,
			loc.Range.End.Line, loc.Range.End.Character,
			comma)
	}
	_, err := fmt.Fprint(w, "]\n")
	return err
}

// uriToPath converts a file:// URI to an absolute file path. If the
// URI is not a file URI, it is returned unchanged.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		// Fallback: strip the scheme prefix manually.
		return strings.TrimPrefix(uri, "file://")
	}
	return u.Path
}

// makeRelative returns a relative path from the current working directory
// to path if possible, otherwise returns the absolute path.
func makeRelative(path string) string {
	rel, err := filepath.Rel(".", path)
	if err != nil {
		return path
	}
	return rel
}

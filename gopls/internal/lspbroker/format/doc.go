// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package format provides output formatters for broker CLI responses.
//
// Each formatter converts structured broker results into human-readable
// text lines (default) or JSON (when the caller requests --json output).
//
// All formatters follow the conventions in designs/06-cli-surface.md:
//
//   - Positions are displayed 1-based (matching editor conventions).
//   - File URIs are converted to relative paths when possible.
//   - Multiple results are printed one per line.
//   - Empty results print nothing and the caller should exit with code 1.
package format

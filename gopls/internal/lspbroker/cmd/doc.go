// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cmd implements the gopls lspcli and gopls lspbrokerd
// subcommand dispatchers.
//
// The subcommands themselves are registered with gopls's
// golang.org/x/tools/internal/tool framework in
// gopls/internal/cmd/lspbroker.go. That file owns the
// tool.Application wrappers; this package provides the
// implementation hooks they delegate into, so the broker's logic
// stays out of the shared gopls/internal/cmd package.
//
// In Phase 0 the entrypoints are near-stubs: --help prints usage
// (handled automatically by the tool framework) and every other
// invocation reports that the feature is not yet implemented. Real
// dispatch lands as the corresponding workstreams implement their
// operations.
package cmd

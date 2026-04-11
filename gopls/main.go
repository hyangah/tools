// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Gopls (pronounced “go please”) is an LSP server for Go.
// The Language Server Protocol allows any text editor
// to be extended with IDE-like features;
// see https://langserver.org/ for details.
//
// See https://go.dev/gopls for comprehensive documentation on Gopls.
package main

import (
	"context"
	"log"
	"os"

	"golang.org/x/telemetry"
	"golang.org/x/telemetry/counter"
	"golang.org/x/tools/gopls/internal/cmd"
	"golang.org/x/tools/gopls/internal/filecache"
	versionpkg "golang.org/x/tools/gopls/internal/version"
	"golang.org/x/tools/internal/tool"
)

var version = "" // if set by the linker, overrides the gopls version

func main() {
	versionpkg.VersionOverride = version

	telemetry.Start(telemetry.Config{
		ReportCrashes: true,
		Upload:        true,
	})

	// Force early creation of the filecache and refuse to start
	// if there were unexpected errors such as ENOSPC. This
	// minimizes the window of exposure to deletion of the
	// executable, and ensures that all subsequent calls to
	// filecache.Get cannot fail for these two reasons;
	// see issue #67433.
	//
	// This leaves only one likely cause for later failures:
	// deletion of the cache while gopls is running. If the
	// problem continues, we could periodically stat the cache
	// directory (for example at the start of every RPC) and
	// either re-create it or just fail the RPC with an
	// informative error and terminate the process.
	//
	// The lspcli and lspbrokerd subcommands do not use gopls's
	// filecache (they forward to a broker daemon over a socket and
	// any gopls work happens in a separate gopls serve process), so
	// we skip the probe for them to preserve their ~20 ms CLI cold
	// start target. See DECISIONS.md ADR-003 and the resolution of
	// OPEN_QUESTIONS.md#Q19 for the rationale; filecache.getCacheDir
	// sha256-hashes the 40 MB gopls binary on first call, which is
	// unacceptable overhead for a short-lived CLI invocation that
	// doesn't consult the cache at all.
	if !lspbrokerSubcommand(os.Args[1:]) {
		if _, err := filecache.Get("nonesuch", [32]byte{}, filecache.Bytes); err != nil && err != filecache.ErrNotFound {
			counter.Inc("gopls/nocache")
			log.Fatalf("gopls cannot access its persistent index (disk full?): %v", err)
		}
	}

	ctx := context.Background()
	tool.Main(ctx, cmd.New(), os.Args[1:])
}

// lspbrokerSubcommand reports whether the gopls invocation is one of
// the LSP-broker subcommands ("lspcli" or "lspbrokerd"), which must
// not pay the filecache-probe cost in main. Kept as a string check on
// os.Args rather than a cmd.Application reflection so the decision
// happens before any gopls package-init that touches the filecache.
func lspbrokerSubcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "lspcli", "lspbrokerd":
		return true
	}
	return false
}

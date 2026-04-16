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
	"strings"

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
	// With -remote=<addr>, this process forwards LSP calls to a daemon
	// and never touches the local filecache, so the ~20 ms smoke test
	// is pure overhead on every CLI invocation. The daemon itself runs
	// this check at its own startup. Skip here in that case.
	if !hasRemoteFlag(os.Args[1:]) {
		if _, err := filecache.Get("nonesuch", [32]byte{}, filecache.Bytes); err != nil && err != filecache.ErrNotFound {
			counter.Inc("gopls/nocache")
			log.Fatalf("gopls cannot access its persistent index (disk full?): %v", err)
		}
	}

	ctx := context.Background()
	tool.Main(ctx, cmd.New(), os.Args[1:])
}

// hasRemoteFlag reports whether args contains a non-empty -remote flag
// (forms: -remote=V, --remote=V, -remote V, --remote V, where V != "").
// Scans only leading flags (stops at the first non-flag, which is the
// subcommand name).
func hasRemoteFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			return false
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			if name[:eq] == "remote" {
				return name[eq+1:] != ""
			}
			continue
		}
		if name == "remote" && i+1 < len(args) {
			return args[i+1] != ""
		}
	}
	return false
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"io"
)

// RunLSPBrokerd is the entry point for `gopls lspbrokerd`. It runs
// the long-lived broker daemon.
//
// Phase 0: the daemon is not yet implemented. This entry point
// prints a stub message and returns nil so that callers verifying
// the self-spawn flow (os.Executable() + "lspbrokerd --detach") see
// an exit-code-0 process and can assert that re-invocation of the
// gopls binary works end-to-end. WS-A fills in the real body in
// Phase 1.
func RunLSPBrokerd(ctx context.Context, args ...string) error {
	fmt.Println("lspbrokerd: Phase 0 stub — daemon not yet implemented")
	return nil
}

// PrintLSPBrokerdHelp writes the lspbrokerd long-form help to w. It
// is invoked by the tool.Application wrapper's DetailedHelp method
// in gopls/internal/cmd/lspbroker.go.
func PrintLSPBrokerdHelp(w io.Writer) {
	fmt.Fprint(w, `
lspbrokerd is the long-lived broker daemon for the LSP broker. It
is normally spawned automatically by "gopls lspcli" on first use
and rarely invoked by hand.

The daemon manages LSP server subprocesses on behalf of shell-based
AI agents, listens on a per-user unix domain socket under
$XDG_CACHE_HOME/lsp-broker/<buildid>/, and idles itself out after a
configurable timeout.

Phase 0: the daemon is not yet implemented. "gopls lspbrokerd
--help" exists so that the subcommand is wired into the gopls
binary and so that self-spawn verification tests can exercise
os.Executable() + "lspbrokerd --detach" end-to-end.
`)
}

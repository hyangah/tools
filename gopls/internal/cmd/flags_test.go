// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

// This file tests CLI flag behavior — in particular, that global flags
// declared on the top-level Application may be passed either before or
// after the subcommand name. See gopls/doc/design/flags.md.

import (
	"testing"
)

// TestFlag_globalFlagBeforeOrAfterSubcommand verifies that a global flag
// like -v is accepted both before and after the subcommand name, and that
// the two invocations produce identical output.
func TestFlag_globalFlagBeforeOrAfterSubcommand(t *testing.T) {
	t.Parallel()

	tree := writeTree(t, "")

	before := gopls(t, tree, "-v", "version")
	before.checkExit(true)
	before.checkStdout("Build info")

	after := gopls(t, tree, "version", "-v")
	after.checkExit(true)
	after.checkStdout("Build info")

	if before.stdout != after.stdout {
		t.Errorf("stdout differs between `gopls -v version` and `gopls version -v`:\nbefore=<<%s>>\nafter=<<%s>>",
			before.stdout, after.stdout)
	}
}

// TestFlag_globalAndSubcommandFlagsTogether verifies that a global flag
// and a subcommand-specific flag can be supplied together after the
// subcommand name.
func TestFlag_globalAndSubcommandFlagsTogether(t *testing.T) {
	t.Parallel()

	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package  a
`)
	// -v is global, -l is format-specific. Both should parse.
	res := gopls(t, tree, "format", "-v", "-l", "a.go")
	res.checkExit(true)
	res.checkStdout("a.go")
}

// TestFlag_unknownFlagStillFails verifies that an unknown flag on a
// subcommand is still rejected (inheritance must not accidentally accept
// everything).
func TestFlag_unknownFlagStillFails(t *testing.T) {
	t.Parallel()

	tree := writeTree(t, "")

	res := gopls(t, tree, "version", "-notaflag")
	res.checkExit(false)
	res.checkStderr("flag provided but not defined")
}

// TestFlag_helpListsInheritedFlags verifies that `gopls help <subcommand>`
// lists global flags alongside the subcommand's own flags.
func TestFlag_helpListsInheritedFlags(t *testing.T) {
	t.Parallel()

	tree := writeTree(t, "")

	res := gopls(t, tree, "help", "version")
	// `help` dispatches via `tool.Run(... "-h")`, so output goes to stderr
	// via the FlagSet's usage; flag.ErrHelp exits 0.
	res.checkExit(true)
	// -json is version-specific; -v/-verbose and -remote are global.
	res.checkStderr("-json")
	res.checkStderr("-v")
	res.checkStderr("-verbose")
	res.checkStderr("-remote")
}

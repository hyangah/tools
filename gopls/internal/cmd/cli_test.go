// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

// This file defines integration tests for the `gopls cli` subcommand.
// Each test runs in two modes:
//   - "in-process": gopls cli is invoked without -remote, so the LSP
//     connection bypasses JSON-RPC marshaling.
//   - "remote": gopls cli is invoked with -remote=unix;<sock> pointing at
//     a test-managed daemon, so responses cross the JSON-RPC boundary.
//
// The remote mode is what catches bugs that only manifest when typed
// values arrive as map[string]any or when a server populates a different
// field than the printer reads.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startTestDaemon returns the -remote=... flag value pointing at a shared
// gopls daemon, starting one lazily on first call. The daemon runs for the
// lifetime of the test binary; TestMain shuts it down via daemonShutdownHook.
func startTestDaemon(t *testing.T) string {
	daemonOnce.Do(initSharedDaemon)
	if daemonErr != nil {
		t.Fatalf("starting test daemon: %v", daemonErr)
	}
	return daemonRemote
}

var (
	daemonOnce      sync.Once
	daemonRemote    string // e.g. "-remote=unix;/tmp/gopls-cli-test-<pid>.sock"
	daemonErr       error
	daemonStdout    bytes.Buffer
	daemonStderr    bytes.Buffer
	daemonSockPath  string
	daemonProcess   *exec.Cmd
	daemonProcessWG sync.WaitGroup
)

func initSharedDaemon() {
	// Use /tmp explicitly: t.TempDir() on macOS lives under /var/folders/...
	// which can exceed the unix socket path length limit (~104 bytes).
	daemonSockPath = filepath.Join("/tmp", fmt.Sprintf("gopls-cli-test-%d.sock", os.Getpid()))
	_ = os.Remove(daemonSockPath) // stale leftover from a crashed run
	addr := "unix;" + daemonSockPath

	cmd := exec.Command(os.Args[0], "serve", "-listen", addr)
	cmd.Env = append(os.Environ(), "ENTRYPOINT=goplsMain")
	cmd.Stdout = &daemonStdout
	cmd.Stderr = &daemonStderr
	if err := cmd.Start(); err != nil {
		daemonErr = fmt.Errorf("exec daemon: %w", err)
		return
	}
	daemonProcess = cmd
	daemonProcessWG.Go(func() {
		_ = cmd.Wait()
	})

	// Wait for the daemon to create the socket.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(daemonSockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			daemonErr = fmt.Errorf("timeout waiting for daemon socket %s; stderr=%s",
				daemonSockPath, daemonStderr.String())
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	daemonRemote = "-remote=" + addr
	daemonShutdownHook = stopSharedDaemon
}

func stopSharedDaemon() {
	if daemonProcess == nil {
		return
	}
	_ = daemonProcess.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { daemonProcessWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = daemonProcess.Process.Kill()
		<-done
	}
	_ = os.Remove(daemonSockPath)
}

// cliMode describes one execution mode for a `gopls cli` test.
// prefix is prepended to the gopls argv before "cli ...".
type cliMode struct {
	name   string
	prefix []string
}

// cliModes returns the modes a cli test should run in.
// Tests should call this and use t.Run(mode.name, ...) per entry.
func cliModes(t *testing.T) []cliMode {
	return []cliMode{
		{name: "in-process", prefix: nil},
		{name: "remote", prefix: []string{startTestDaemon(t)}},
	}
}

// runCLI runs `gopls [mode.prefix...] cli <args...>` against tree.
func runCLI(t *testing.T, tree string, mode cliMode, args ...string) *result {
	full := make([]string, 0, len(mode.prefix)+1+len(args))
	full = append(full, mode.prefix...)
	full = append(full, "cli")
	full = append(full, args...)
	return gopls(t, tree, full...)
}

// TestCLIDef tests `gopls cli def` in both FILE:LINE:COL and SYMBOL --in FILE forms.
func TestCLIDef(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func Foo() {}

func bar() {
	Foo()
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			// FILE:LINE:COL form
			{
				res := runCLI(t, tree, mode, "def", "a.go:6:2") // call to Foo
				res.checkExit(true)
				res.checkStdout(`a\.go:3:6`)
			}
			// SYMBOL --in FILE form (exercises DocumentSymbol resolution)
			{
				res := runCLI(t, tree, mode, "def", "Foo", "--in", "a.go")
				res.checkExit(true)
				res.checkStdout(`a\.go:3:6`)
			}
		})
	}
}

// TestCLIRefs tests `gopls cli refs`.
func TestCLIRefs(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func Foo() {}

func bar() {
	Foo()
	Foo()
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "refs", "Foo", "--in", "a.go")
			res.checkExit(true)
			// Expect both call sites and (with IncludeDeclaration) the decl.
			res.checkStdout(`a\.go:3:6`)
			res.checkStdout(`a\.go:6:2`)
			res.checkStdout(`a\.go:7:2`)
		})
	}
}

// TestCLIImpl tests `gopls cli impl`.
func TestCLIImpl(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

type Stringer interface {
	String() string
}

type T struct{}

func (T) String() string { return "" }
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "impl", "Stringer", "--in", "a.go")
			res.checkExit(true)
			res.checkStdout(`a\.go:`)
		})
	}
}

// TestCLIHover tests `gopls cli hover`.
func TestCLIHover(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

// Foo does foo.
func Foo() {}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "hover", "Foo", "--in", "a.go")
			res.checkExit(true)
			res.checkStdout(`Foo`)
		})
	}
}

// TestCLISymbols tests `gopls cli symbols`.
//
// The remote variant is the regression test for the bug where
// DocumentSymbol's []any result arrived as []map[string]any across the
// JSON-RPC boundary, causing the type assertion to drop every entry and
// the command to fail with "symbol not found".
func TestCLISymbols(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func Foo() {}

var V int

const C = 0
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "symbols", "a.go")
			res.checkExit(true)
			// Smoke: output must be non-empty (the regression).
			if res.stdout == "" {
				t.Fatalf("symbols produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			res.checkStdout(`Foo\s+Function`)
			res.checkStdout(`V\s+Variable`)
			res.checkStdout(`C\s+Constant`)
		})
	}
}

// TestCLIWSymbols tests `gopls cli wsymbols`.
func TestCLIWSymbols(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func WSymbolsTestFn() {}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "wsymbols", "WSymbolsTestFn")
			res.checkExit(true)
			res.checkStdout(`WSymbolsTestFn`)
		})
	}
}

// renameSource is the txtar archive for TestCLIRename* tests.
const renameSource = `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func oldname() {}

func caller() {
	oldname()
}
`

// TestCLIRenameDryRun tests `gopls cli rename --dry-run`, which prints a terse
// summary of what would change without modifying any files.
//
// This also serves as the regression test for the bug where the printer
// iterated WorkspaceEdit.Changes (always empty under gopls) instead of
// DocumentChanges, producing empty stdout.
func TestCLIRenameDryRun(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, renameSource)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "rename", "--dry-run", "oldname", "--in", "a.go", "--to", "newname")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("rename --dry-run produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// Expect both edit sites: the declaration and the caller.
			res.checkStdout(`a\.go`)
			res.checkStdout(`"newname"`)
			// Dry-run must not write to disk: a.go must still contain "oldname".
			content, err := os.ReadFile(filepath.Join(tree, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(content, []byte("oldname")) {
				t.Errorf("--dry-run modified a.go on disk; content=%q", content)
			}
		})
	}
}

// TestCLIRenameWrite tests `gopls cli rename -w`, which overwrites files
// in place with the renamed content.
func TestCLIRenameWrite(t *testing.T) {
	t.Parallel()
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			// Give each mode its own tree so -w does not corrupt siblings.
			tree := writeTree(t, renameSource)
			res := runCLI(t, tree, mode, "rename", "-w", "oldname", "--in", "a.go", "--to", "newname")
			res.checkExit(true)
			content, err := os.ReadFile(filepath.Join(tree, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(content, []byte("newname")) {
				t.Errorf("-w did not write newname to a.go; content=%q", content)
			}
			if bytes.Contains(content, []byte("oldname")) {
				t.Errorf("-w left oldname in a.go; content=%q", content)
			}
		})
	}
}

// TestCLIRenameDiff tests `gopls cli rename -d`, which prints a unified diff
// of the changes without modifying any files.
func TestCLIRenameDiff(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, renameSource)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "rename", "-d", "oldname", "--in", "a.go", "--to", "newname")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("rename -d produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// Unified diff must contain standard diff markers.
			res.checkStdout(`---`)
			res.checkStdout(`\+\+\+`)
			res.checkStdout(`@@`)
			res.checkStdout(`newname`)
			// Must not modify disk.
			content, err := os.ReadFile(filepath.Join(tree, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(content, []byte("oldname")) {
				t.Errorf("-d modified a.go on disk; content=%q", content)
			}
		})
	}
}

// TestCLIRenameList tests `gopls cli rename -l`, which prints the names of
// files that would change without modifying any files.
func TestCLIRenameList(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, renameSource)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "rename", "-l", "oldname", "--in", "a.go", "--to", "newname")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("rename -l produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// -l prints file names only.
			res.checkStdout(`a\.go`)
			// Must not modify disk.
			content, err := os.ReadFile(filepath.Join(tree, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(content, []byte("oldname")) {
				t.Errorf("-l modified a.go on disk; content=%q", content)
			}
		})
	}
}

// TestCLIRenameDefault tests `gopls cli rename` with no edit flags, which
// prints the full new content of each changed file to stdout.
func TestCLIRenameDefault(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, renameSource)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "rename", "oldname", "--in", "a.go", "--to", "newname")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("rename produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// Default output is the full new file content; it must contain newname.
			res.checkStdout(`newname`)
			// Must not modify disk.
			content, err := os.ReadFile(filepath.Join(tree, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(content, []byte("oldname")) {
				t.Errorf("no-flag rename modified a.go on disk; content=%q", content)
			}
		})
	}
}

// TestCLICheck exercises `gopls cli check` in both workspace-pull form
// (no args, calls workspace/diagnostic) and per-file pull form (args,
// calls textDocument/diagnostic). Pins the pull-diagnostic plumbing
// end-to-end: if wantsPullDiagnostics doesn't thread through, the server
// won't advertise diagnosticProvider and this will fail.
func TestCLICheck(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func Foo() int {
	var x int
	return x + undefined
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			// Workspace pull.
			{
				res := runCLI(t, tree, mode, "check")
				res.checkExit(true)
				if res.stdout == "" {
					t.Fatalf("check (workspace) produced empty stdout in mode %s; stderr=%s",
						mode.name, res.stderr)
				}
				res.checkStdout(`a\.go:`)
				res.checkStdout(`undefined`)
			}
			// Per-file pull.
			{
				res := runCLI(t, tree, mode, "check", "./a.go")
				res.checkExit(true)
				if res.stdout == "" {
					t.Fatalf("check (per-file) produced empty stdout in mode %s; stderr=%s",
						mode.name, res.stderr)
				}
				res.checkStdout(`a\.go:`)
				res.checkStdout(`undefined`)
			}
		})
	}
}

// TestCLIFormat exercises `gopls cli format`. It verifies both the
// default (print reformatted content) and the -l (list changed files)
// modes, using a source file with deliberate bad formatting. Also
// verifies end-to-end that the edit-producing server path works with
// the CLI profile's skipDidOpen=true: if the Formatting handler needed
// an overlay, this test would fail.
func TestCLIFormat(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- bad.go --
package a

func Bad( ) {
var x int
_=x
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			// Default: print reformatted content.
			{
				res := runCLI(t, tree, mode, "format", "./bad.go")
				res.checkExit(true)
				if res.stdout == "" {
					t.Fatalf("format produced empty stdout in mode %s; stderr=%s",
						mode.name, res.stderr)
				}
				res.checkStdout(`func Bad\(\) \{`)
				res.checkStdout(`_ = x`)
			}
			// -l: list changed files.
			{
				res := runCLI(t, tree, mode, "format", "-l", "./bad.go")
				res.checkExit(true)
				res.checkStdout(`bad\.go`)
			}
		})
	}
}

// TestCLIImports exercises `gopls cli imports`, verifying that an
// unused import is removed. Goes through textDocument/codeAction with
// Only=SourceOrganizeImports.
func TestCLIImports(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

import (
	"fmt"
	"strings"
)

func F() {
	fmt.Println("hi")
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "imports", "./a.go")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("imports produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// The unused "strings" import must be removed.
			if strings.Contains(res.stdout, `"strings"`) {
				t.Errorf("unused import not removed; stdout=%q", res.stdout)
			}
			res.checkStdout(`"fmt"`)
		})
	}
}

// TestCLICodeAction exercises `gopls cli codeaction` listing code
// actions at a file position. Uses a known quickfix-producing
// situation (unused variable) so the list is non-empty.
func TestCLICodeAction(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

func F() {
	x := 1
	_ = x
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			// Position at func F opening brace — gopls commonly surfaces
			// refactor actions there.
			res := runCLI(t, tree, mode, "codeaction", "./a.go:3:6")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("codeaction produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
		})
	}
}

// TestCLIFix exercises `gopls cli fix` applying the SourceOrganizeImports
// code action via --kind. Chosen for test stability: organize-imports is
// deterministic, always present on a file with imports, and inline (no
// resolve needed in gopls today).
func TestCLIFix(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

import (
	"fmt"
	"strings"
)

func F() {
	fmt.Println("hi")
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "fix", "./a.go:1:1", "--kind", "source.organizeImports")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("fix produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			if strings.Contains(res.stdout, `"strings"`) {
				t.Errorf("unused import not removed; stdout=%q", res.stdout)
			}
			res.checkStdout(`"fmt"`)
		})
	}
}

// TestCLIVet exercises `gopls cli vet`, verifying that the Source
// filter keeps vet-suite findings (printf) and drops non-vet ones
// (type-check errors from the compiler).
func TestCLIVet(t *testing.T) {
	t.Parallel()
	tree := writeTree(t, `
-- go.mod --
module example.com
go 1.18

-- a.go --
package a

import "fmt"

func Bad() {
	fmt.Printf("%d", "not a number")
}
`)
	for _, mode := range cliModes(t) {
		t.Run(mode.name, func(t *testing.T) {
			res := runCLI(t, tree, mode, "vet", "./a.go")
			res.checkExit(true)
			if res.stdout == "" {
				t.Fatalf("vet produced empty stdout in mode %s; stderr=%s",
					mode.name, res.stderr)
			}
			// printf analyzer catches the format/arg mismatch and is in
			// the vet suite, so it must appear.
			res.checkStdout(`a\.go:`)
			res.checkStdout(`printf`)
		})
	}
}

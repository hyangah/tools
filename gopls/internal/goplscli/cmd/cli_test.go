// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
	clicmd "golang.org/x/tools/gopls/internal/goplscli/cmd"
)

func testFiles(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	files := map[string]string{
		"go.mod": "module example.com/test\n\ngo 1.21\n",
		"main.go": `package main

import "fmt"

// Greeting returns a greeting.
func Greeting(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	msg := Greeting("world")
	fmt.Println(msg)
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func startServer(t *testing.T) (string, string) {
	t.Helper()
	root := testFiles(t)
	c := cache.New(nil)
	// Use a short socket path to avoid exceeding macOS's 104-byte limit.
	sockDir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	addr := filepath.Join(sockDir, "s.sock")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	go goplscli.Serve(ctx, addr, c)

	for range 50 {
		if _, err := os.Stat(addr); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, root
}

func runCLI(t *testing.T, addr string, jsonOut bool, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	code := clicmd.Run(t.Context(), addr, jsonOut, args, &buf)
	return buf.String(), code
}

func TestCLISync(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")
	out, code := runCLI(t, addr, false, "sync", mainFile)
	if code != 0 {
		t.Fatalf("sync exit %d: %s", code, out)
	}
}

func TestCLIDefinition(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "def", mainFile+":11:9")
	if code != 0 {
		t.Fatalf("def exit %d: %s", code, out)
	}
	t.Logf("def output: %s", out)
	if !strings.Contains(out, ":6:") {
		t.Errorf("expected definition at line 6, got: %s", out)
	}
}

func TestCLIDefinitionJSON(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, true, "def", mainFile+":11:9")
	if code != 0 {
		t.Fatalf("def --json exit %d: %s", code, out)
	}
	if !strings.Contains(out, `"Line": 6`) {
		t.Errorf("expected JSON with Line 6, got: %s", out)
	}
}

func TestCLIHover(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "hover", mainFile+":6:6")
	if code != 0 {
		t.Fatalf("hover exit %d: %s", code, out)
	}
	t.Logf("hover output:\n%s", out)
	if !strings.Contains(out, "Greeting") {
		t.Errorf("expected hover to mention Greeting, got: %s", out)
	}
}

func TestCLISymbols(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "symbols", mainFile)
	if code != 0 {
		t.Fatalf("symbols exit %d: %s", code, out)
	}
	t.Logf("symbols output:\n%s", out)
	if !strings.Contains(out, "Greeting") || !strings.Contains(out, "main") {
		t.Errorf("expected Greeting and main symbols, got: %s", out)
	}
}

func TestCLIDiagnostics(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "diagnostics", mainFile)
	if code != 0 {
		t.Fatalf("diagnostics exit %d: %s", code, out)
	}
	// Valid code should produce no error diagnostics.
	t.Logf("diagnostics output: %q", out)
}

func TestCLIUnknownCommand(t *testing.T) {
	addr, _ := startServer(t)
	_, code := runCLI(t, addr, false, "bogus")
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
}

func TestCLIMissingArgs(t *testing.T) {
	addr, _ := startServer(t)
	_, code := runCLI(t, addr, false, "def")
	if code != 2 {
		t.Errorf("expected exit code 2 for missing args, got %d", code)
	}
}

func TestCLIDefinitionBySymbol(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "def", "Greeting", "--in", mainFile)
	if code != 0 {
		t.Fatalf("def by symbol exit %d: %s", code, out)
	}
	t.Logf("def by symbol output: %s", out)
	if !strings.Contains(out, ":6:") {
		t.Errorf("expected definition at line 6, got: %s", out)
	}
}

func TestCLIDefSymbolLine(t *testing.T) {
	addr, root := startServer(t)
	mainFile := filepath.Join(root, "main.go")

	out, code := runCLI(t, addr, false, "def", "Greeting", "--in", mainFile+":6")
	if code != 0 {
		t.Fatalf("def by symbol with line exit %d: %s", code, out)
	}
	t.Logf("def by symbol with line output: %s", out)
	if !strings.Contains(out, ":6:") {
		t.Errorf("expected definition at line 6, got: %s", out)
	}
}

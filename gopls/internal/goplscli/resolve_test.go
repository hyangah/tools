// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
)

func TestResolveSymbolExactMatch(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	_, rng, err := goplscli.ResolveSymbol(ctx, gs, mainFile, "Greeting", 0)
	if err != nil {
		t.Fatalf("ResolveSymbol: %v", err)
	}
	// Greeting is declared at line 6 (1-based) = line 5 (0-based).
	if rng.Start.Line != 5 {
		t.Errorf("expected symbol at 0-based line 5, got %d", rng.Start.Line)
	}
	t.Logf("Greeting found at 0-based line %d, char %d", rng.Start.Line, rng.Start.Character)
}

func TestResolveSymbolWithLineNarrowing(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	_, rng, err := goplscli.ResolveSymbol(ctx, gs, mainFile, "Greeting", 6)
	if err != nil {
		t.Fatalf("ResolveSymbol with line: %v", err)
	}
	if rng.Start.Line != 5 {
		t.Errorf("expected symbol at 0-based line 5, got %d", rng.Start.Line)
	}
}

func TestResolveSymbolNotFound(t *testing.T) {
	ctx := t.Context()
	root := testFiles(t)
	c := cache.New(nil)
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}

	mainFile := filepath.Join(root, "main.go")
	if err := gs.EnsureSynced(ctx, mainFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	_, _, resolveErr := goplscli.ResolveSymbol(ctx, gs, mainFile, "NonExistent", 0)
	if resolveErr == nil {
		t.Fatal("expected error for non-existent symbol")
	}
	if !strings.Contains(resolveErr.Error(), "symbol not found") {
		t.Errorf("expected 'symbol not found' error, got: %v", resolveErr)
	}
}

// TestResolveSymbolMethodSuffix verifies that resolving a method name
// matches gopls's "(T).Method" naming convention via suffix matching.
func TestResolveSymbolMethodSuffix(t *testing.T) {
	ctx := t.Context()

	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	files := map[string]string{
		"go.mod": "module example.com/methods\n\ngo 1.21\n",
		"methods.go": `package methods

type Greeter struct{}

func (g Greeter) Hello() string {
	return "hello"
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	c := cache.New(nil)
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}

	methodsFile := filepath.Join(root, "methods.go")
	if err := gs.EnsureSynced(ctx, methodsFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	// Resolve by bare method name "Hello" — should match "(Greeter).Hello"
	// via the suffix matching in matchesSymbolName.
	_, rng, err := goplscli.ResolveSymbol(ctx, gs, methodsFile, "Hello", 0)
	if err != nil {
		t.Fatalf("ResolveSymbol(Hello): %v", err)
	}
	// Hello is on line 5 (1-based) = line 4 (0-based).
	if rng.Start.Line != 4 {
		t.Errorf("expected method at 0-based line 4, got %d", rng.Start.Line)
	}
}

func TestResolveSymbolAmbiguous(t *testing.T) {
	ctx := t.Context()

	// Create a module with two top-level symbols named "Name".
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	files := map[string]string{
		"go.mod": "module example.com/ambig\n\ngo 1.21\n",
		"ambig.go": `package ambig

type A struct {
	Name string
}

type B struct {
	Name string
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	c := cache.New(nil)
	gs, err := goplscli.NewInProcessServer(ctx, c, root)
	if err != nil {
		t.Fatalf("NewInProcessServer: %v", err)
	}

	ambigFile := filepath.Join(root, "ambig.go")
	if err := gs.EnsureSynced(ctx, ambigFile); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	// Without line narrowing, should be ambiguous.
	_, _, err = goplscli.ResolveSymbol(ctx, gs, ambigFile, "Name", 0)
	if err == nil {
		t.Fatal("expected error for ambiguous symbol")
	}
	if !strings.Contains(err.Error(), "ambiguous symbol") {
		t.Errorf("expected 'ambiguous symbol' error, got: %v", err)
	}

	// With line narrowing to line 4 (first Name field), should resolve.
	_, rng, err := goplscli.ResolveSymbol(ctx, gs, ambigFile, "Name", 4)
	if err != nil {
		t.Fatalf("ResolveSymbol with line narrowing: %v", err)
	}
	if rng.Start.Line != 3 { // 0-based
		t.Errorf("expected 0-based line 3, got %d", rng.Start.Line)
	}
}

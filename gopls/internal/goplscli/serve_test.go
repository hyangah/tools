// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/goplscli"
)

// startTestServer starts a CLI protocol server on a temp unix socket
// and returns the address. The server is stopped when the test finishes.
func startTestServer(t *testing.T, c *cache.Cache) string {
	t.Helper()

	sockDir := t.TempDir()
	addr := filepath.Join(sockDir, "cli.sock")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		errCh <- goplscli.Serve(ctx, addr, c)
	}()

	// Wait for the socket to appear.
	for i := range 50 {
		if _, err := os.Stat(addr); err == nil {
			break
		}
		if i == 49 {
			t.Fatal("timed out waiting for server socket")
		}
		time.Sleep(10 * time.Millisecond)
	}

	return addr
}

func TestServeDefinition(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	// First sync the file.
	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodSync,
		File:   mainFile,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("sync error: %s", resp.Error)
	}

	// Query definition of "Greeting" at line 11, col 9 (1-based).
	resp, err = goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodDefinition,
		File:   mainFile,
		Line:   11,
		Column: 9,
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("definition error: %s", resp.Error)
	}
	if len(resp.Locations) == 0 {
		t.Fatal("definition returned no locations")
	}

	loc := resp.Locations[0]
	t.Logf("Definition: %s:%d:%d", loc.File, loc.Start.Line, loc.Start.Column)
	if loc.Start.Line != 6 {
		t.Errorf("expected definition at line 6, got %d", loc.Start.Line)
	}
}

func TestServeHover(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodHover,
		File:   mainFile,
		Line:   6,
		Column: 6,
	})
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("hover error: %s", resp.Error)
	}
	if resp.Hover == nil {
		t.Fatal("hover returned nil")
	}
	t.Logf("Hover sig: %s", resp.Hover.Signature)
	t.Logf("Hover doc: %s", resp.Hover.Doc)
	if resp.Hover.Signature == "" {
		t.Error("expected non-empty signature")
	}
}

func TestServeReferences(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	// Sync both files first.
	for _, f := range []string{"main.go", "main_test.go"} {
		resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
			Method: goplscli.MethodSync,
			File:   filepath.Join(root, f),
		})
		if err != nil {
			t.Fatalf("sync %s: %v", f, err)
		}
		if resp.Error != "" {
			t.Fatalf("sync %s error: %s", f, resp.Error)
		}
	}

	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method:             goplscli.MethodReferences,
		File:               mainFile,
		Line:               6,
		Column:             6,
		IncludeDeclaration: true,
	})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("references error: %s", resp.Error)
	}
	t.Logf("References: %d results", len(resp.Locations))
	for _, loc := range resp.Locations {
		t.Logf("  %s:%d:%d", loc.File, loc.Start.Line, loc.Start.Column)
	}
	if len(resp.Locations) < 2 {
		t.Errorf("expected at least 2 references, got %d", len(resp.Locations))
	}
}

func TestServeSymbols(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodSymbols,
		File:   mainFile,
	})
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("symbols error: %s", resp.Error)
	}
	t.Logf("Symbols: %d results", len(resp.Symbols))
	for _, s := range resp.Symbols {
		t.Logf("  %s (%s) %s:%d", s.Name, s.Kind, s.Location.File, s.Location.Start.Line)
	}
	if len(resp.Symbols) < 2 {
		t.Errorf("expected at least 2 symbols, got %d", len(resp.Symbols))
	}
}

func TestServeDiagnostics(t *testing.T) {
	root := testFiles(t)
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	mainFile := filepath.Join(root, "main.go")

	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: goplscli.MethodDiagnostics,
		File:   mainFile,
	})
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("diagnostics error: %s", resp.Error)
	}
	// Valid code should have no error diagnostics.
	for _, d := range resp.Diagnostics {
		t.Logf("  %s:%d:%d %s: %s", d.File, d.Line, d.Column, d.Severity, d.Message)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	c := cache.New(nil)
	addr := startTestServer(t, c)

	ctx := t.Context()
	resp, err := goplscli.SendRequest(ctx, addr, &goplscli.Request{
		Method: "nonexistent",
		File:   "/tmp/fake.go",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected error for unknown method")
	}
	t.Logf("Error (expected): %s", resp.Error)
}

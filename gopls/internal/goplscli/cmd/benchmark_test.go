// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	cli "golang.org/x/tools/gopls/internal/goplscli/cmd"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/server"
	"golang.org/x/tools/gopls/internal/settings"
	"golang.org/x/tools/internal/testenv"
)

// benchClient is a minimal protocol.ClientCloser for benchmarks.
type benchClient struct{ protocol.Client }

func (benchClient) Close() error { return nil }
func (benchClient) PublishDiagnostics(context.Context, *protocol.PublishDiagnosticsParams) error {
	return nil
}
func (benchClient) ShowMessage(context.Context, *protocol.ShowMessageParams) error { return nil }
func (benchClient) Progress(context.Context, *protocol.ProgressParams) error       { return nil }
func (benchClient) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}
func (benchClient) RegisterCapability(context.Context, *protocol.RegistrationParams) error {
	return nil
}
func (benchClient) UnregisterCapability(context.Context, *protocol.UnregistrationParams) error {
	return nil
}
func (benchClient) Configuration(context.Context, *protocol.ParamConfiguration) ([]protocol.LSPAny, error) {
	return nil, nil
}

// TestCLIBenchmark measures the performance of various CLI operations
// against the gopls codebase. This is not a Go benchmark (b.N loop)
// because the cold start must be measured exactly once.
//
// Run with: go test -run TestCLIBenchmark -v -count=1
func TestCLIBenchmark(t *testing.T) {
	testenv.NeedsTool(t, "go")

	// Use the gopls directory as workspace for meaningful benchmarks.
	root := os.Getenv("GOPLS_BENCH_ROOT")
	if root == "" {
		t.Skip("set GOPLS_BENCH_ROOT to the gopls workspace root to run benchmarks")
	}

	ctx := t.Context()

	// Target file for benchmarks.
	targetFile := root + "/internal/cache/session.go"
	if _, err := os.Stat(targetFile); err != nil {
		t.Fatalf("target file %s not found: %v", targetFile, err)
	}

	// Create an in-process server (simulates -remote=auto with pool).
	c := cache.New(nil)
	sess := cache.NewSession(ctx, c)
	options := settings.DefaultOptions(nil)
	svr := server.New(sess, benchClient{}, options)

	// Initialize the server.
	initStart := time.Now()
	params := &protocol.ParamInitialize{}
	params.RootURI = protocol.URIFromPath(root)
	params.WorkspaceFolders = []protocol.WorkspaceFolder{
		{URI: string(protocol.URIFromPath(root)), Name: "gopls"},
	}
	params.Capabilities.TextDocument.DocumentSymbol.HierarchicalDocumentSymbolSupport = true
	_, err := svr.Initialize(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := svr.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		t.Fatal(err)
	}
	initDur := time.Since(initStart)
	t.Logf("Cold start (Initialize + IWL): %v", initDur)

	// Open the target file (required for in-process mode).
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	uri := protocol.URIFromPath(targetFile)
	if err := svr.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        uri,
			LanguageID: "go",
			Version:    1,
			Text:       string(content),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Warm up by running a definition query.
	var buf bytes.Buffer
	cli.Run(ctx, svr, false, []string{"def", targetFile + ":39:6"}, &buf)
	t.Logf("Warmup def result: %s", buf.String())

	// Benchmark each operation.
	commands := []struct {
		name string
		args []string
	}{
		{"def", []string{"def", targetFile + ":39:6"}},
		{"refs", []string{"refs", targetFile + ":39:6"}},
		{"hover", []string{"hover", targetFile + ":39:6"}},
		{"symbols", []string{"symbols", targetFile}},
		{"wsymbols", []string{"wsymbols", "NewSession"}},
		{"impl", []string{"impl", targetFile + ":609:6"}},
		{"def-symbol", []string{"def", "NewSession", "--in", targetFile}},
	}

	const runs = 5
	for _, cmd := range commands {
		var total time.Duration
		for i := range runs {
			buf.Reset()
			start := time.Now()
			exitCode := cli.Run(ctx, svr, false, cmd.args, &buf)
			dur := time.Since(start)
			if exitCode != 0 {
				t.Errorf("%s: exit code %d (run %d)", cmd.name, exitCode, i)
				continue
			}
			total += dur
		}
		avg := total / runs
		t.Logf("%-12s  avg=%v  total=%v  (%d runs)", cmd.name, avg, total, runs)
	}

	// Print summary in a format comparable to v3 benchmarks.
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "=== v4 CLI Benchmark Summary ===")
	fmt.Fprintf(os.Stderr, "Cold start: %v\n", initDur)
	for _, cmd := range commands {
		var total time.Duration
		for range runs {
			buf.Reset()
			start := time.Now()
			cli.Run(ctx, svr, false, cmd.args, &buf)
			total += time.Since(start)
		}
		fmt.Fprintf(os.Stderr, "%-12s  %v (avg of %d)\n", cmd.name, total/runs, runs)
	}
}

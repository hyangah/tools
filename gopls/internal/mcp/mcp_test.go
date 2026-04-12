// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mcp_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/tools/gopls/internal/cache"
	internalmcp "golang.org/x/tools/gopls/internal/mcp"
	"golang.org/x/tools/gopls/internal/protocol"
)

type emptySessions struct {
}

// FirstSession implements mcp.Sessions.
func (e emptySessions) FirstSession() (*cache.Session, protocol.Server) {
	return nil, nil
}

// Session implements mcp.Sessions.
func (e emptySessions) Session(string) (*cache.Session, protocol.Server) {
	return nil, nil
}

// SetSessionExitFunc implements mcp.Sessions.
func (e emptySessions) SetSessionExitFunc(func(string)) {
}

// TestToolsDocumented verifies that all MCP tools defined in code are
// documented in gopls/doc/features/mcp.md by extracting tool names
// directly from mcp.go source.
func TestToolsDocumented(t *testing.T) {
	// Read mcp.go source to extract tool names from case statements
	mcpSrc, err := os.ReadFile("gopls/internal/mcp/mcp.go")
	if err != nil {
		t.Fatalf("could not read mcp.go: %v", err)
	}

	// Extract tool names from case statements in addToolByName function
	// Look for: case "go_<name>":
	content := string(mcpSrc)

	// Find all case statements in addToolByName
	// Pattern: case "go_<name>":
	start := strings.Index(content, "func addToolByName")
	if start == -1 {
		t.Fatalf("addToolByName function not found")
	}
	end := strings.Index(content[start:], "\n}\n")
	if end == -1 {
		t.Fatalf("addToolByName function end not found")
	}
	funcBody := content[start : start+end+3]

	var tools []string
	lines := strings.Split(funcBody, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "case \"go_") && strings.HasSuffix(trimmed, "\":") {
			toolName := strings.TrimPrefix(strings.TrimSuffix(trimmed, "\":"), "case \"")
			tools = append(tools, toolName)
		}
	}

	if len(tools) == 0 {
		t.Fatalf("no tools found in addToolByName")
	}

	// Read the documentation file
	docPath := "gopls/doc/features/mcp.md"
	docContent, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("could not read %s: %v", docPath, err)
	}
	docStr := string(docContent)

	// Verify markers are present
	if !strings.Contains(docStr, "<!-- BEGIN_MCP_TOOLS -->") || !strings.Contains(docStr, "<!-- END_MCP_TOOLS -->") {
		t.Errorf("MCP tools markers not found in documentation")
	}

	// Check that all tools from code are documented
	for _, tool := range tools {
		if !strings.Contains(docStr, "**"+tool+"**") {
			t.Errorf("tool %q not documented in mcp.md", tool)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	res := make(chan error)
	go func() {
		res <- internalmcp.Serve(ctx, "localhost:0", emptySessions{}, true, nil)
	}()

	time.Sleep(1 * time.Second)
	cancel()

	select {
	case err := <-res:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("mcp server unexpected return got %v, want: %v", err, http.ErrServerClosed)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("mcp server did not terminate after 5 seconds of context cancellation")
	}
}

func TestClientRootChange(t *testing.T) {
	foundError := make(chan error, 1)
	foundAll := []chan struct{}{
		make(chan struct{}), // after initialized
		make(chan struct{}), // after roots/list_changed
	}

	wantRoots := []map[string]struct{}{
		{ // after initialized
			"file:///path/to/one": struct{}{},
			"file:///path/to/two": struct{}{},
		},
		{ // after roots/list_changed
			"file:///path/to/one":   struct{}{},
			"file:///path/to/two":   struct{}{},
			"file:///path/to/three": struct{}{},
			"file:///path/to/four":  struct{}{},
		},
	}

	var callCount int

	server := internalmcp.NewServer(nil, nil, func(res *mcp.ListRootsResult, err error) {
		if err != nil {
			foundError <- err
			return
		}

		if callCount >= len(wantRoots) {
			t.Errorf("Handler called more times than expected: %d", callCount)
			return
		}

		expected := wantRoots[callCount]

		if len(res.Roots) != len(expected) {
			t.Errorf("Phase %d: expected %d roots, got %d", callCount+1, len(expected), len(res.Roots))
			return
		}

		for _, r := range res.Roots {
			if _, ok := expected[r.URI]; !ok {
				t.Errorf("Phase %d: unexpected root %s", callCount+1, r)
			}
		}

		callCount++
		close(foundAll[callCount-1]) // Signal that this phase is complete
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	client.AddRoots(&mcp.Root{
		Name: "one",
		URI:  "file:///path/to/one",
	}, &mcp.Root{
		Name: "two",
		URI:  "file:///path/to/two",
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx := t.Context()

	// Connect server and client
	serverSession, _ := server.Connect(ctx, serverTransport, nil)
	defer serverSession.Close()

	clientSession, _ := client.Connect(ctx, clientTransport, nil)
	defer clientSession.Close()

	// Phase 1: Wait for the initial handshake and first root fetch
	select {
	case <-foundAll[0]:
		t.Log("Phase 1: Initial roots received successfully.")
	case err := <-foundError:
		t.Fatalf("Server handler error during initialization: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Timeout waiting for initial roots.")
	}

	// Trigger the root change
	client.AddRoots(&mcp.Root{
		Name: "three",
		URI:  "file:///path/to/three",
	}, &mcp.Root{
		Name: "four",
		URI:  "file:///path/to/four",
	})

	// Phase 2: Wait for the server to catch the notification and fetch the updated roots
	select {
	case <-foundAll[1]:
		t.Log("Phase 2: Updated roots received successfully.")
	case err := <-foundError:
		t.Fatalf("Server handler error after root change: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Timeout waiting for updated roots.")
	}
}

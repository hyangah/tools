// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command fakelsp is a minimal LSP server for testing. It speaks
// JSON-RPC 2.0 over stdio with Content-Length framing and responds to:
//
//   - initialize: returns basic capabilities
//   - initialized: no-op
//   - textDocument/didOpen: records the file
//   - textDocument/definition: returns a canned location (line 0, char 0 of the same file)
//   - textDocument/documentSymbol: returns a single symbol "FakeSymbol" at line 0
//   - textDocument/hover: returns a canned hover
//   - shutdown / exit: clean exit
//
// It is not a real language server. It exists only for broker integration
// tests that need a non-Go LSP server.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		// Read Content-Length header.
		contentLen := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return // stdin closed
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break // end of headers
			}
			if strings.HasPrefix(line, "Content-Length:") {
				n, _ := strconv.Atoi(strings.TrimSpace(line[len("Content-Length:"):]))
				contentLen = n
			}
		}
		if contentLen == 0 {
			continue
		}

		// Read body.
		body := make([]byte, contentLen)
		if _, err := reader.Read(body); err != nil {
			return
		}

		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		// Notifications (no ID) — just consume them.
		if msg.ID == nil || string(msg.ID) == "null" {
			if msg.Method == "exit" {
				os.Exit(0)
			}
			continue
		}

		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{
				"capabilities": map[string]any{
					"definitionProvider":     true,
					"documentSymbolProvider": true,
					"hoverProvider":          true,
					"referencesProvider":     true,
				},
			}
		case "shutdown":
			result = nil
		case "textDocument/definition":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			json.Unmarshal(msg.Params, &p)
			result = []map[string]any{{
				"uri": p.TextDocument.URI,
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 10},
				},
			}}
		case "textDocument/documentSymbol":
			result = []map[string]any{{
				"name": "FakeSymbol",
				"kind": 12, // Function
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 10},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 10},
				},
			}}
		case "textDocument/hover":
			result = map[string]any{
				"contents": map[string]any{
					"kind":  "markdown",
					"value": "Fake hover content",
				},
			}
		case "textDocument/references":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			json.Unmarshal(msg.Params, &p)
			result = []map[string]any{{
				"uri": p.TextDocument.URI,
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 10},
				},
			}}
		default:
			// Method not found — return error.
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found: " + msg.Method,
				},
			}
			writeResponse(resp)
			continue
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result":  result,
		}
		writeResponse(resp)
	}
}

func writeResponse(resp any) {
	data, _ := json.Marshal(resp)
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	os.Stdout.WriteString(header)
	os.Stdout.Write(data)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
)

// Serve starts the CLI protocol listener on the given unix socket address.
// It creates a [CLIHandler] backed by the shared cache and serves
// connections until the context is canceled.
func Serve(ctx context.Context, address string, c *cache.Cache) error {
	network, addr := parseListenAddress(address)

	// Remove stale socket file before binding. If gopls crashed
	// previously (SIGKILL, panic), defer os.Remove was skipped.
	if network == "unix" {
		os.Remove(addr)
	}

	ln, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("goplscli: listen %s %s: %w", network, addr, err)
	}
	defer ln.Close()
	if network == "unix" {
		defer os.Remove(addr)
	}

	log.Printf("Gopls CLI daemon: listening on %s %s", network, addr)
	defer log.Printf("Gopls CLI daemon: exiting")

	handler := NewCLIHandler(c)
	defer handler.Close(ctx)

	// Close the listener when the context is canceled.
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // clean shutdown
			default:
				return fmt.Errorf("goplscli: accept: %w", err)
			}
		}
		go serveConn(ctx, handler, conn)
	}
}

// serveConn handles a single CLI connection: read one request, dispatch,
// write one response, close.
func serveConn(ctx context.Context, handler *CLIHandler, conn net.Conn) {
	defer conn.Close()

	req, err := readRequest(conn)
	if err != nil {
		writeResponse(conn, &Response{Error: fmt.Sprintf("read request: %v", err)})
		return
	}

	resp := dispatch(ctx, handler, req)
	if err := writeResponse(conn, resp); err != nil {
		log.Printf("goplscli: write response: %v", err)
	}
}

// dispatch routes a request to the appropriate handler method.
func dispatch(ctx context.Context, h *CLIHandler, req *Request) *Response {
	if req.File == "" && req.Method != MethodWSymbols {
		return &Response{Error: "file is required"}
	}

	switch req.Method {
	case MethodSync:
		return handleSync(ctx, h, req)
	case MethodDefinition:
		return handleLocations(ctx, h, req, MethodDefinition)
	case MethodReferences:
		return handleLocations(ctx, h, req, MethodReferences)
	case MethodImplementation:
		return handleLocations(ctx, h, req, MethodImplementation)
	case MethodHover:
		return handleHover(ctx, h, req)
	case MethodSymbols:
		return handleSymbols(ctx, h, req)
	case MethodWSymbols:
		return handleWSymbols(ctx, h, req)
	case MethodDiagnostics:
		return handleDiagnostics(ctx, h, req)
	case MethodRename:
		return handleRename(ctx, h, req)
	default:
		return &Response{Error: fmt.Sprintf("unknown method: %q", req.Method)}
	}
}

func handleSync(ctx context.Context, h *CLIHandler, req *Request) *Response {
	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.ForceSync(ctx, req.File); err != nil {
		return &Response{Error: err.Error()}
	}
	return &Response{}
}

func handleLocations(ctx context.Context, h *CLIHandler, req *Request, method string) *Response {
	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.EnsureSynced(ctx, req.File); err != nil {
		return &Response{Error: fmt.Sprintf("sync: %v", err)}
	}

	uri := protocol.URIFromPath(req.File)

	// Get mapper for position conversion.
	mapper, err := mapperForURI(ctx, gs, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	// Convert CLI position to protocol position.
	pos := CLIToProtocol(mapper, CLIPosition{Line: req.Line, Column: req.Column})
	rng := protocol.Range{Start: pos, End: pos}

	var locs []protocol.Location
	switch method {
	case MethodDefinition:
		locs, err = gs.Definition(ctx, uri, rng)
	case MethodReferences:
		locs, err = gs.References(ctx, uri, rng, req.IncludeDeclaration)
	case MethodImplementation:
		locs, err = gs.Implementation(ctx, uri, rng)
	}
	if err != nil {
		return &Response{Error: err.Error()}
	}

	// Convert locations to CLI format.
	cliLocs := make([]CLILocation, 0, len(locs))
	for _, loc := range locs {
		m, err := mapperForURI(ctx, gs, loc.URI)
		if err != nil {
			log.Printf("goplscli: skipping location %s: %v", loc.URI, err)
			continue
		}
		cl, err := LocationToCLI(m, loc)
		if err != nil {
			log.Printf("goplscli: position conversion for %s: %v", loc.URI, err)
			continue
		}
		cliLocs = append(cliLocs, cl)
	}
	return &Response{Locations: cliLocs}
}

func handleHover(ctx context.Context, h *CLIHandler, req *Request) *Response {
	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.EnsureSynced(ctx, req.File); err != nil {
		return &Response{Error: fmt.Sprintf("sync: %v", err)}
	}

	uri := protocol.URIFromPath(req.File)
	mapper, err := mapperForURI(ctx, gs, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	pos := CLIToProtocol(mapper, CLIPosition{Line: req.Line, Column: req.Column})
	rng := protocol.Range{Start: pos, End: pos}

	hover, err := gs.Hover(ctx, uri, rng)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if hover == nil {
		return &Response{} // no hover info
	}

	// Parse gopls's hover markdown format: ```go\nsig\n``` \n---\n doc \n---\n link.
	// This is coupled to golang.Hover's internal formatting. If gopls changes
	// the hover format, this parsing will produce degraded (but not incorrect)
	// output — the raw markdown falls through to Signature.
	result := &HoverResult{}
	content := hover.Contents.Value
	// gopls returns markdown with signature in a code block, then doc.
	if idx := strings.Index(content, "\n\n---\n\n"); idx >= 0 {
		result.Signature = strings.TrimPrefix(content[:idx], "```go\n")
		result.Signature = strings.TrimSuffix(result.Signature, "\n```")
		rest := content[idx+len("\n\n---\n\n"):]
		// Check for a second --- separator (pkg.go.dev link).
		if idx2 := strings.Index(rest, "\n\n---\n\n"); idx2 >= 0 {
			result.Doc = strings.TrimSpace(rest[:idx2])
			result.Link = strings.TrimSpace(rest[idx2+len("\n\n---\n\n"):])
		} else {
			result.Doc = strings.TrimSpace(rest)
		}
	} else {
		result.Signature = content
	}

	return &Response{Hover: result}
}

func handleSymbols(ctx context.Context, h *CLIHandler, req *Request) *Response {
	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.EnsureSynced(ctx, req.File); err != nil {
		return &Response{Error: fmt.Sprintf("sync: %v", err)}
	}

	uri := protocol.URIFromPath(req.File)
	mapper, err := mapperForURI(ctx, gs, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	symbols, err := gs.DocumentSymbols(ctx, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	results := convertDocSymbols(symbols, req.File, mapper)
	return &Response{Symbols: results}
}

func convertDocSymbols(symbols []protocol.DocumentSymbol, file string, mapper *protocol.Mapper) []SymbolResult {
	results := make([]SymbolResult, len(symbols))
	for i, s := range symbols {
		startPos, _ := ProtocolToCLI(mapper, s.Range.Start)
		endPos, _ := ProtocolToCLI(mapper, s.Range.End)
		results[i] = SymbolResult{
			Name: s.Name,
			Kind: symbolKindString(s.Kind),
			Location: CLILocation{
				File:  file,
				Start: startPos,
				End:   endPos,
			},
			Children: convertDocSymbols(s.Children, file, mapper),
		}
	}
	return results
}

func handleWSymbols(ctx context.Context, h *CLIHandler, req *Request) *Response {
	// Workspace symbols need a session — use file if provided, otherwise
	// any existing session.
	var gs *GoSession
	var err error
	if req.File != "" {
		gs, err = h.SessionForFile(ctx, req.File)
	} else if roots := h.Roots(); len(roots) > 0 {
		gs, err = h.SessionFor(ctx, roots[0])
	} else {
		return &Response{Error: "no workspace root available for wsymbols"}
	}
	if err != nil {
		return &Response{Error: err.Error()}
	}

	symbols, err := gs.WorkspaceSymbols(ctx, req.Query)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	results := make([]WorkspaceSymbolResult, len(symbols))
	for i, s := range symbols {
		m, _ := mapperForURI(ctx, gs, s.Location.URI)
		var startPos, endPos CLIPosition
		if m != nil {
			startPos, _ = ProtocolToCLI(m, s.Location.Range.Start)
			endPos, _ = ProtocolToCLI(m, s.Location.Range.End)
		}
		results[i] = WorkspaceSymbolResult{
			Name:          s.Name,
			Kind:          symbolKindString(s.Kind),
			ContainerName: s.ContainerName,
			Location: CLILocation{
				File:  s.Location.URI.Path(),
				Start: startPos,
				End:   endPos,
			},
		}
	}
	return &Response{WorkspaceSymbols: results}
}

func handleDiagnostics(ctx context.Context, h *CLIHandler, req *Request) *Response {
	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.EnsureSynced(ctx, req.File); err != nil {
		return &Response{Error: fmt.Sprintf("sync: %v", err)}
	}

	uri := protocol.URIFromPath(req.File)
	mapper, err := mapperForURI(ctx, gs, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	diags, err := gs.DiagnoseFile(ctx, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	results := make([]DiagnosticResult, len(diags))
	for i, d := range diags {
		results[i] = convertCacheDiag(d, mapper)
	}
	return &Response{Diagnostics: results}
}

func handleRename(ctx context.Context, h *CLIHandler, req *Request) *Response {
	if req.NewName == "" {
		return &Response{Error: "newName is required for rename"}
	}

	gs, err := h.SessionForFile(ctx, req.File)
	if err != nil {
		return &Response{Error: err.Error()}
	}
	if err := gs.EnsureSynced(ctx, req.File); err != nil {
		return &Response{Error: fmt.Sprintf("sync: %v", err)}
	}

	uri := protocol.URIFromPath(req.File)
	mapper, err := mapperForURI(ctx, gs, uri)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	pos := CLIToProtocol(mapper, CLIPosition{Line: req.Line, Column: req.Column})
	rng := protocol.Range{Start: pos, End: pos}

	changes, err := gs.Rename(ctx, uri, rng, req.NewName)
	if err != nil {
		return &Response{Error: err.Error()}
	}

	// Convert DocumentChanges to CLI format.
	var fileEdits []RenameFileEdit
	for _, dc := range changes {
		tde := dc.TextDocumentEdit
		if tde == nil {
			continue
		}
		fileURI := tde.TextDocument.URI
		m, err := mapperForURI(ctx, gs, fileURI)
		if err != nil {
			continue
		}
		fe := RenameFileEdit{File: fileURI.Path()}
		for _, edit := range tde.Edits {
			var te protocol.TextEdit
			switch e := edit.Value.(type) {
			case protocol.TextEdit:
				te = e
			case protocol.AnnotatedTextEdit:
				te = e.TextEdit
			default:
				continue
			}
			start, _ := ProtocolToCLI(m, te.Range.Start)
			end, _ := ProtocolToCLI(m, te.Range.End)
			fe.Edits = append(fe.Edits, RenameEdit{
				Start:   start,
				End:     end,
				NewText: te.NewText,
			})
		}
		fileEdits = append(fileEdits, fe)
	}
	return &Response{RenameEdits: fileEdits}
}

// mapperForURI returns a Mapper for the file at the given URI.
func mapperForURI(ctx context.Context, gs *GoSession, uri protocol.DocumentURI) (*protocol.Mapper, error) {
	fh, snapshot, release, err := gs.Session().FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	_ = snapshot
	content, err := fh.Content()
	if err != nil {
		return nil, err
	}
	return protocol.NewMapper(uri, content), nil
}

// Wire framing: 4-byte big-endian length prefix + JSON payload.

const maxMessageSize = 10 * 1024 * 1024 // 10MB

func readMessage(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length > maxMessageSize {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	return buf, nil
}

func writeMessage(w io.Writer, data []byte) error {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readRequest(r io.Reader) (*Request, error) {
	buf, err := readMessage(r)
	if err != nil {
		return nil, err
	}
	var req Request
	if err := json.Unmarshal(buf, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &req, nil
}

func writeResponse(w io.Writer, resp *Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return writeMessage(w, data)
}

// parseListenAddress parses "unix;/path/to/sock" or "tcp;host:port".
// Default is unix.
func parseListenAddress(addr string) (network, address string) {
	if i := strings.IndexByte(addr, ';'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	// Default to unix socket.
	return "unix", addr
}

// SendRequest connects to the daemon at the given address, sends the
// request, and returns the response. Used by CLI commands.
func SendRequest(ctx context.Context, address string, req *Request) (*Response, error) {
	network, addr := parseListenAddress(address)

	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := writeMessage(conn, data); err != nil {
		return nil, err
	}

	return readResponse(conn)
}

func readResponse(r io.Reader) (*Response, error) {
	buf, err := readMessage(r)
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

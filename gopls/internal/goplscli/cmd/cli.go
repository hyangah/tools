// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cmd implements the `gopls cli` subcommand, which provides
// agent-friendly Go code intelligence via the gopls daemon.
//
// Unlike the legacy `gopls definition` commands, `gopls cli` supports
// symbol-based lookup (e.g., `gopls cli def Parse --in file.go`) and
// produces terse output suitable for AI agents and shell scripts.
//
// The CLI connects to the gopls daemon via standard LSP using -remote=auto,
// benefiting from session pooling when enabled.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
)

// Run is the entry point for `gopls cli <subcommand> ...`.
// It dispatches to the appropriate handler and writes output to w.
// The server is an LSP server connection (from app.connect or similar).
func Run(ctx context.Context, server protocol.Server, jsonOutput bool, args []string, w io.Writer) (exitCode int) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gopls cli <command> [args]")
		fmt.Fprintln(os.Stderr, "commands: def, refs, hover, impl, symbols, wsymbols, rename, check, vet, format, imports")
		return 2
	}

	sub, subArgs := args[0], args[1:]

	// Edit-producing subcommands (format, imports, fix) handle their own
	// output because the edit-mode flags (-w/-d/-l) control side effects
	// and per-file rendering that don't fit the result→printer pipeline.
	switch sub {
	case "format":
		code, err := runFormat(ctx, server, jsonOutput, subArgs, w)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return code
	case "imports":
		code, err := runImports(ctx, server, jsonOutput, subArgs, w)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return code
	}

	var result any
	var err error

	switch sub {
	case "def":
		result, err = runLocationCommand(ctx, server, "textDocument/definition", subArgs)
	case "refs":
		result, err = runLocationCommand(ctx, server, "textDocument/references", subArgs)
	case "hover":
		result, err = runHover(ctx, server, subArgs)
	case "impl":
		result, err = runLocationCommand(ctx, server, "textDocument/implementation", subArgs)
	case "symbols":
		result, err = runSymbols(ctx, server, subArgs)
	case "wsymbols":
		result, err = runWSymbols(ctx, server, subArgs)
	case "rename":
		result, err = runRename(ctx, server, subArgs)
	case "check":
		result, err = runCheck(ctx, server, subArgs)
	case "vet":
		result, err = runVet(ctx, server, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", sub)
		return 2
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		printText(w, sub, result)
	}
	return 0
}

// resolvePosition parses args into a text document identifier and range,
// supporting both FILE:LINE:COL and SYMBOL --in FILE forms.
// gopls uses Range (not Position) for all query methods.
func resolvePosition(ctx context.Context, server protocol.Server, args []string) (protocol.TextDocumentPositionParams, error) {
	var positional, inSpec string
	for i := 0; i < len(args); i++ {
		if args[i] == "--in" && i+1 < len(args) {
			inSpec = args[i+1]
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			if positional != "" {
				return protocol.TextDocumentPositionParams{}, fmt.Errorf("multiple positional arguments")
			}
			positional = args[i]
		}
	}
	if positional == "" {
		return protocol.TextDocumentPositionParams{}, fmt.Errorf("usage: FILE:LINE:COL or SYMBOL --in FILE")
	}

	// Try FILE:LINE:COL first.
	if inSpec == "" {
		parts := splitFileLineCol(positional)
		if len(parts) == 3 {
			if _, err1 := strconv.Atoi(parts[1]); err1 == nil {
				if _, err2 := strconv.Atoi(parts[2]); err2 == nil {
					file, line, col, err := parseFileLineCol(positional)
					if err != nil {
						return protocol.TextDocumentPositionParams{}, err
					}
					uri := protocol.URIFromPath(file)
					pos := protocol.Position{
						Line:      uint32(line - 1),
						Character: uint32(col - 1),
					}
					return protocol.TextDocumentPositionParams{
						TextDocument: protocol.TextDocumentIdentifier{URI: uri},
						Position:     pos,
						Range:        protocol.Range{Start: pos, End: pos},
					}, nil
				}
			}
		}
		return protocol.TextDocumentPositionParams{}, fmt.Errorf("usage: FILE:LINE:COL or SYMBOL --in FILE")
	}

	// Symbol-based: positional is symbol name, inSpec is FILE or FILE:LINE.
	symbolName := positional
	parts := splitFileLineCol(inSpec)
	var filePath string
	var line int

	switch len(parts) {
	case 1:
		filePath = parts[0]
	case 2, 3:
		filePath = parts[0]
		var err error
		line, err = strconv.Atoi(parts[1])
		if err != nil {
			return protocol.TextDocumentPositionParams{}, fmt.Errorf("invalid line %q in %q", parts[1], inSpec)
		}
	default:
		return protocol.TextDocumentPositionParams{}, fmt.Errorf("invalid file spec %q", inSpec)
	}

	filePath, err := filepath.Abs(filePath)
	if err != nil {
		return protocol.TextDocumentPositionParams{}, err
	}

	uri, rng, err := goplscli.ResolveSymbol(ctx, server, filePath, symbolName, line)
	if err != nil {
		return protocol.TextDocumentPositionParams{}, err
	}
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position:     rng.Start,
		Range:        rng,
	}, nil
}

// runLocationCommand runs definition, references, or implementation.
func runLocationCommand(ctx context.Context, server protocol.Server, method string, args []string) ([]goplscli.CLILocation, error) {
	tdpp, err := resolvePosition(ctx, server, args)
	if err != nil {
		return nil, err
	}

	var locs []protocol.Location
	switch method {
	case "textDocument/definition":
		locs, err = server.Definition(ctx, &protocol.DefinitionParams{
			TextDocumentPositionParams: tdpp,
		})
	case "textDocument/references":
		locs, err = server.References(ctx, &protocol.ReferenceParams{
			TextDocumentPositionParams: tdpp,
			Context: protocol.ReferenceContext{
				IncludeDeclaration: true,
			},
		})
	case "textDocument/implementation":
		locs, err = server.Implementation(ctx, &protocol.ImplementationParams{
			TextDocumentPositionParams: tdpp,
		})
	}
	if err != nil {
		return nil, err
	}
	result := make([]goplscli.CLILocation, len(locs))
	for i, loc := range locs {
		result[i] = goplscli.LocationToCLI(loc)
	}
	return result, nil
}

// runHover runs textDocument/hover.
func runHover(ctx context.Context, server protocol.Server, args []string) (*hoverResult, error) {
	tdpp, err := resolvePosition(ctx, server, args)
	if err != nil {
		return nil, err
	}
	hover, err := server.Hover(ctx, &protocol.HoverParams{
		TextDocumentPositionParams: tdpp,
	})
	if err != nil {
		return nil, err
	}
	if hover == nil {
		return nil, fmt.Errorf("no hover information at this position")
	}
	return &hoverResult{Content: hover.Contents.Value}, nil
}

type hoverResult struct {
	Content string `json:"content"`
}

// runSymbols runs textDocument/documentSymbol.
func runSymbols(ctx context.Context, server protocol.Server, args []string) ([]protocol.DocumentSymbol, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: gopls cli symbols FILE")
	}
	file, err := filepath.Abs(args[0])
	if err != nil {
		return nil, err
	}
	uri := protocol.URIFromPath(file)
	result, err := server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return nil, err
	}
	return goplscli.DecodeDocumentSymbols(result)
}

// runWSymbols runs workspace/symbol.
func runWSymbols(ctx context.Context, server protocol.Server, args []string) ([]protocol.SymbolInformation, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: gopls cli wsymbols QUERY")
	}
	return server.Symbol(ctx, &protocol.WorkspaceSymbolParams{
		Query: args[0],
	})
}

// runRename runs textDocument/rename.
func runRename(ctx context.Context, server protocol.Server, args []string) (*protocol.WorkspaceEdit, error) {
	// Parse: FILE:LINE:COL --to NEWNAME  or  SYMBOL --in FILE --to NEWNAME
	var newName string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--to" && i+1 < len(args) {
			newName = args[i+1]
			i++
		} else {
			posArgs = append(posArgs, args[i])
		}
	}
	if newName == "" {
		return nil, fmt.Errorf("usage: gopls cli rename FILE:LINE:COL --to NEWNAME")
	}

	tdpp, err := resolvePosition(ctx, server, posArgs)
	if err != nil {
		return nil, err
	}
	return server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: tdpp,
		NewName:                    newName,
	})
}

// printText prints the result in human-readable text format.
func printText(w io.Writer, sub string, result any) {
	switch sub {
	case "def", "refs", "impl":
		locs := result.([]goplscli.CLILocation)
		for _, loc := range locs {
			fmt.Fprintf(w, "%s:%d:%d\n", loc.File, loc.Start.Line, loc.Start.Column)
		}
	case "hover":
		h := result.(*hoverResult)
		fmt.Fprintln(w, h.Content)
	case "symbols":
		symbols := result.([]protocol.DocumentSymbol)
		for _, s := range symbols {
			printDocSymbol(w, s, 0)
		}
	case "wsymbols":
		symbols := result.([]protocol.SymbolInformation)
		for _, s := range symbols {
			loc := goplscli.LocationToCLI(s.Location)
			fmt.Fprintf(w, "%s\t%s\t%s:%d:%d\n", s.Name, s.Kind, loc.File, loc.Start.Line, loc.Start.Column)
		}
	case "rename":
		edit := result.(*protocol.WorkspaceEdit)
		printRenameEdit(w, edit)
	case "check", "vet":
		printDiagnostics(w, result.([]cliDiagnostic))
	}
}

// printRenameEdit prints a WorkspaceEdit returned by textDocument/rename.
// gopls always populates DocumentChanges (the modern field), regardless of
// client capability — so we read from there rather than the legacy Changes map.
func printRenameEdit(w io.Writer, edit *protocol.WorkspaceEdit) {
	for _, c := range edit.DocumentChanges {
		if c.TextDocumentEdit == nil {
			continue
		}
		uri := c.TextDocumentEdit.TextDocument.URI
		fmt.Fprintf(w, "%s:\n", uri.Path())
		for _, e := range protocol.AsTextEdits(c.TextDocumentEdit.Edits) {
			fmt.Fprintf(w, "  %d:%d-%d:%d → %q\n",
				e.Range.Start.Line+1, e.Range.Start.Character+1,
				e.Range.End.Line+1, e.Range.End.Character+1,
				e.NewText)
		}
	}
	for uri, edits := range edit.Changes {
		fmt.Fprintf(w, "%s:\n", uri.Path())
		for _, e := range edits {
			fmt.Fprintf(w, "  %d:%d-%d:%d → %q\n",
				e.Range.Start.Line+1, e.Range.Start.Character+1,
				e.Range.End.Line+1, e.Range.End.Character+1,
				e.NewText)
		}
	}
}

func printDocSymbol(w io.Writer, s protocol.DocumentSymbol, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(w, "%s%s\t%s\t%d:%d\n", indent, s.Name, s.Kind, s.Range.Start.Line+1, s.Range.Start.Character+1)
	for _, child := range s.Children {
		printDocSymbol(w, child, depth+1)
	}
}

// parseFileLineCol parses "file:line:col" into components.
func parseFileLineCol(spec string) (file string, line, col int, err error) {
	parts := splitFileLineCol(spec)
	if len(parts) < 3 {
		return "", 0, 0, fmt.Errorf("expected FILE:LINE:COL, got %q", spec)
	}
	file = parts[0]
	line, err = strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid line %q in %q", parts[1], spec)
	}
	col, err = strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid column %q in %q", parts[2], spec)
	}
	file, err = filepath.Abs(file)
	if err != nil {
		return "", 0, 0, err
	}
	return file, line, col, nil
}

// splitFileLineCol splits "file:line:col" handling colons in paths.
func splitFileLineCol(spec string) []string {
	i := strings.LastIndexByte(spec, ':')
	if i < 0 {
		return []string{spec}
	}
	j := strings.LastIndexByte(spec[:i], ':')
	if j < 0 {
		return []string{spec[:i], spec[i+1:]}
	}
	return []string{spec[:j], spec[j+1 : i], spec[i+1:]}
}

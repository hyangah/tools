// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cmd implements the `gopls cli` subcommand, which provides
// Go code intelligence for AI coding agents via the goplscli daemon.
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
)

// Run is the entry point for `gopls cli <subcommand> ...`.
// The address is the unix socket to connect to.
func Run(ctx context.Context, address string, jsonOutput bool, args []string, w io.Writer) (exitCode int) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gopls cli <command> [args]")
		fmt.Fprintln(os.Stderr, "commands: def, refs, hover, impl, symbols, wsymbols, rename, sync, diagnostics")
		return 2
	}

	sub, subArgs := args[0], args[1:]

	var req *goplscli.Request
	var err error

	switch sub {
	case "def":
		req, err = parsePosCommand(goplscli.MethodDefinition, subArgs)
	case "refs":
		req, err = parsePosCommand(goplscli.MethodReferences, subArgs)
		if req != nil {
			req.IncludeDeclaration = true
		}
	case "hover":
		req, err = parsePosCommand(goplscli.MethodHover, subArgs)
	case "impl":
		req, err = parsePosCommand(goplscli.MethodImplementation, subArgs)
	case "symbols":
		req, err = parseFileCommand(goplscli.MethodSymbols, subArgs)
	case "wsymbols":
		req, err = parseQueryCommand(goplscli.MethodWSymbols, subArgs)
	case "rename":
		req, err = parseRenameCommand(subArgs)
	case "sync":
		req, err = parseFileCommand(goplscli.MethodSync, subArgs)
	case "diagnostics":
		req, err = parseFileCommand(goplscli.MethodDiagnostics, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", sub)
		return 2
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	resp, err := goplscli.SendRequest(ctx, address, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		return 3
	}

	if jsonOutput {
		return printJSON(w, resp, sub)
	}
	return printText(w, resp, sub)
}

// parsePosCommand parses `<command> FILE:LINE:COL` or `<command> --in FILE:LINE:COL`.
func parsePosCommand(method string, args []string) (*goplscli.Request, error) {
	var fileSpec string

	// Support both `def FILE:LINE:COL` and `def --in FILE:LINE:COL`.
	for i := 0; i < len(args); i++ {
		if args[i] == "--in" && i+1 < len(args) {
			fileSpec = args[i+1]
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			if fileSpec != "" {
				return nil, fmt.Errorf("multiple file arguments")
			}
			fileSpec = args[i]
		}
	}
	if fileSpec == "" {
		return nil, fmt.Errorf("usage: gopls cli %s FILE:LINE:COL", method)
	}

	file, line, col, err := parseFileLineCol(fileSpec)
	if err != nil {
		return nil, err
	}

	return &goplscli.Request{
		Method: method,
		File:   file,
		Line:   line,
		Column: col,
	}, nil
}

// parseFileCommand parses `<command> FILE`.
func parseFileCommand(method string, args []string) (*goplscli.Request, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: gopls cli %s FILE", method)
	}
	file, err := filepath.Abs(args[0])
	if err != nil {
		return nil, err
	}
	return &goplscli.Request{
		Method: method,
		File:   file,
	}, nil
}

// parseQueryCommand parses `<command> QUERY`.
func parseQueryCommand(method string, args []string) (*goplscli.Request, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: gopls cli %s QUERY", method)
	}
	return &goplscli.Request{
		Method: method,
		Query:  args[0],
	}, nil
}

// parseRenameCommand parses `rename FILE:LINE:COL --to NEWNAME`.
func parseRenameCommand(args []string) (*goplscli.Request, error) {
	var fileSpec, newName string
	for i := 0; i < len(args); i++ {
		if args[i] == "--to" && i+1 < len(args) {
			newName = args[i+1]
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			fileSpec = args[i]
		}
	}
	if fileSpec == "" || newName == "" {
		return nil, fmt.Errorf("usage: gopls cli rename FILE:LINE:COL --to NEWNAME")
	}

	file, line, col, err := parseFileLineCol(fileSpec)
	if err != nil {
		return nil, err
	}

	return &goplscli.Request{
		Method:  goplscli.MethodRename,
		File:    file,
		Line:    line,
		Column:  col,
		NewName: newName,
	}, nil
}

// parseFileLineCol parses "file:line:col" into components.
// Line and col are 1-based.
func parseFileLineCol(spec string) (file string, line, col int, err error) {
	// Split from the right to handle Windows paths like C:\foo\bar.go:10:5.
	parts := splitFileLineCol(spec)
	if len(parts) < 3 {
		return "", 0, 0, fmt.Errorf("expected FILE:LINE:COL, got %q", spec)
	}

	file = parts[0]
	line, err = strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid line number %q in %q", parts[1], spec)
	}
	col, err = strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid column number %q in %q", parts[2], spec)
	}

	file, err = filepath.Abs(file)
	if err != nil {
		return "", 0, 0, err
	}

	return file, line, col, nil
}

// splitFileLineCol splits "file:line:col" handling the case where file
// might contain colons (e.g., Windows paths).
func splitFileLineCol(spec string) []string {
	// Find the last two colons.
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

// printJSON outputs the response as JSON.
func printJSON(w io.Writer, resp *goplscli.Response, method string) int {
	var data any
	switch method {
	case "def", "refs", "impl":
		data = resp.Locations
		if len(resp.Locations) == 0 {
			return 1 // empty result
		}
	case "hover":
		data = resp.Hover
		if resp.Hover == nil {
			return 1
		}
	case "symbols":
		data = resp.Symbols
	case "wsymbols":
		data = resp.WorkspaceSymbols
	case "diagnostics":
		data = resp.Diagnostics
	case "rename":
		data = resp.RenameEdits
	case "sync":
		return 0
	default:
		data = resp
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(data)
	return 0
}

// printText outputs the response as human-readable text.
func printText(w io.Writer, resp *goplscli.Response, method string) int {
	switch method {
	case "def", "refs", "impl":
		if len(resp.Locations) == 0 {
			return 1
		}
		for _, loc := range resp.Locations {
			fmt.Fprintf(w, "%s:%d:%d\n", loc.File, loc.Start.Line, loc.Start.Column)
		}
	case "hover":
		if resp.Hover == nil {
			return 1
		}
		if resp.Hover.Signature != "" {
			fmt.Fprintln(w, resp.Hover.Signature)
		}
		if resp.Hover.Doc != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, resp.Hover.Doc)
		}
	case "symbols":
		for _, s := range resp.Symbols {
			printSymbol(w, s, 0)
		}
	case "wsymbols":
		for _, s := range resp.WorkspaceSymbols {
			fmt.Fprintf(w, "%s\t%s\t%s:%d:%d\n", s.Name, s.Kind, s.Location.File, s.Location.Start.Line, s.Location.Start.Column)
		}
	case "diagnostics":
		for _, d := range resp.Diagnostics {
			fmt.Fprintf(w, "%s:%d:%d: %s: %s\n", d.File, d.Line, d.Column, d.Severity, d.Message)
		}
	case "rename":
		for _, fe := range resp.RenameEdits {
			fmt.Fprintf(w, "%s:\n", fe.File)
			for _, e := range fe.Edits {
				fmt.Fprintf(w, "  %d:%d-%d:%d → %q\n", e.Start.Line, e.Start.Column, e.End.Line, e.End.Column, e.NewText)
			}
		}
	case "sync":
		// no output
	}
	return 0
}

func printSymbol(w io.Writer, s goplscli.SymbolResult, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(w, "%s%s\t%s\t%d:%d\n", indent, s.Name, s.Kind, s.Location.Start.Line, s.Location.Start.Column)
	for _, child := range s.Children {
		printSymbol(w, child, depth+1)
	}
}

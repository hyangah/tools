// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
)

// Wire protocol for CLI ↔ daemon communication.
//
// Each CLI connection sends exactly one Request, receives one Response,
// then closes. The framing is length-prefixed JSON:
//
//	[4-byte big-endian length][JSON payload]
//
// This is intentionally simpler than LSP's Content-Length header framing,
// making protocol detection trivial if we ever multiplex sockets.

// Request is the wire type sent from CLI to daemon.
type Request struct {
	// Method identifies the operation.
	Method string `json:"method"`

	// File is the absolute path to the target file.
	File string `json:"file"`

	// Line is the 1-based line number (for position-based queries).
	Line int `json:"line,omitempty"`

	// Column is the 1-based UTF-8 byte column (for position-based queries).
	Column int `json:"column,omitempty"`

	// Symbol is the symbol name (for name-based queries).
	Symbol string `json:"symbol,omitempty"`

	// Query is a search query (for workspace symbols).
	Query string `json:"query,omitempty"`

	// NewName is the new name (for rename).
	NewName string `json:"newName,omitempty"`

	// IncludeDeclaration controls whether references include the declaration.
	IncludeDeclaration bool `json:"includeDeclaration,omitempty"`

	// DryRun controls whether rename applies changes (false) or just reports them (true).
	DryRun bool `json:"dryRun,omitempty"`
}

// Response is the wire type sent from daemon to CLI.
type Response struct {
	// Error is non-empty if the request failed.
	Error string `json:"error,omitempty"`

	// Locations is set for definition, references, implementation results.
	Locations []CLILocation `json:"locations,omitempty"`

	// Hover is set for hover results.
	Hover *HoverResult `json:"hover,omitempty"`

	// Symbols is set for documentSymbol results.
	Symbols []SymbolResult `json:"symbols,omitempty"`

	// WorkspaceSymbols is set for workspaceSymbol results.
	WorkspaceSymbols []WorkspaceSymbolResult `json:"workspaceSymbols,omitempty"`

	// Diagnostics is set for diagnostics results.
	Diagnostics []DiagnosticResult `json:"diagnostics,omitempty"`

	// RenameEdits is set for rename results.
	RenameEdits []RenameFileEdit `json:"renameEdits,omitempty"`
}

// HoverResult contains hover information.
type HoverResult struct {
	Signature string `json:"signature,omitempty"` // type signature
	Doc       string `json:"doc,omitempty"`       // documentation
	Link      string `json:"link,omitempty"`      // pkg.go.dev link
}

// SymbolResult represents a document symbol.
type SymbolResult struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"` // "Function", "Type", etc.
	Location CLILocation    `json:"location"`
	Children []SymbolResult `json:"children,omitempty"`
}

// WorkspaceSymbolResult represents a workspace symbol.
type WorkspaceSymbolResult struct {
	Name          string      `json:"name"`
	Kind          string      `json:"kind"`
	ContainerName string      `json:"containerName,omitempty"`
	Location      CLILocation `json:"location"`
}

// DiagnosticResult represents a diagnostic.
type DiagnosticResult struct {
	File     string `json:"file"`
	Line     int    `json:"line"`   // 1-based
	Column   int    `json:"column"` // 1-based UTF-8
	EndLine  int    `json:"endLine,omitempty"`
	EndCol   int    `json:"endColumn,omitempty"`
	Severity string `json:"severity"` // "error", "warning", "info", "hint"
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// RenameFileEdit represents edits to a single file from a rename operation.
type RenameFileEdit struct {
	File  string       `json:"file"`
	Edits []RenameEdit `json:"edits"`
}

// RenameEdit is a single text edit within a file.
type RenameEdit struct {
	Start   CLIPosition `json:"start"`
	End     CLIPosition `json:"end"`
	NewText string      `json:"newText"`
}

// Method constants for the wire protocol.
const (
	MethodDefinition     = "definition"
	MethodReferences     = "references"
	MethodHover          = "hover"
	MethodImplementation = "implementation"
	MethodSymbols        = "symbols"
	MethodWSymbols       = "wsymbols"
	MethodRename         = "rename"
	MethodDiagnostics    = "diagnostics"
	MethodSync           = "sync"
)

// diagnosticSeverityString converts a protocol severity to a human-readable string.
func diagnosticSeverityString(s protocol.DiagnosticSeverity) string {
	switch s {
	case protocol.SeverityError:
		return "error"
	case protocol.SeverityWarning:
		return "warning"
	case protocol.SeverityInformation:
		return "info"
	case protocol.SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// cacheDiagnosticSeverityString converts a cache diagnostic severity.
func cacheDiagnosticSeverityString(s protocol.DiagnosticSeverity) string {
	return diagnosticSeverityString(s)
}

// symbolKindString converts a protocol.SymbolKind to a human-readable string.
func symbolKindString(k protocol.SymbolKind) string {
	switch k {
	case protocol.File:
		return "File"
	case protocol.Module:
		return "Module"
	case protocol.Namespace:
		return "Namespace"
	case protocol.Package:
		return "Package"
	case protocol.Class:
		return "Class"
	case protocol.Method:
		return "Method"
	case protocol.Property:
		return "Property"
	case protocol.Field:
		return "Field"
	case protocol.Constructor:
		return "Constructor"
	case protocol.Enum:
		return "Enum"
	case protocol.Interface:
		return "Interface"
	case protocol.Function:
		return "Function"
	case protocol.Variable:
		return "Variable"
	case protocol.Constant:
		return "Constant"
	case protocol.String:
		return "String"
	case protocol.Number:
		return "Number"
	case protocol.Boolean:
		return "Boolean"
	case protocol.Array:
		return "Array"
	case protocol.Object:
		return "Object"
	case protocol.Struct:
		return "Struct"
	case protocol.TypeParameter:
		return "TypeParameter"
	default:
		return "Unknown"
	}
}

// convertCacheDiag converts a [cache.Diagnostic] to a [DiagnosticResult]
// using the given mapper for position conversion.
func convertCacheDiag(d *cache.Diagnostic, mapper *protocol.Mapper) DiagnosticResult {
	startPos, _ := ProtocolToCLI(mapper, d.Range.Start)
	endPos, _ := ProtocolToCLI(mapper, d.Range.End)
	return DiagnosticResult{
		File:     d.URI.Path(),
		Line:     startPos.Line,
		Column:   startPos.Column,
		EndLine:  endPos.Line,
		EndCol:   endPos.Column,
		Severity: cacheDiagnosticSeverityString(d.Severity),
		Message:  d.Message,
		Source:   string(d.Source),
	}
}

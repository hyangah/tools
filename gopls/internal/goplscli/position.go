// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"fmt"

	"golang.org/x/tools/gopls/internal/protocol"
)

// CLIPosition represents a 1-based line and 1-based UTF-8 byte column,
// matching go/token.Position.Column and Go compiler output.
type CLIPosition struct {
	Line   int `json:"line"`   // 1-based
	Column int `json:"column"` // 1-based, UTF-8 byte offset from line start
}

// CLILocation represents a file location with 1-based UTF-8 positions.
type CLILocation struct {
	File  string      `json:"file"`
	Start CLIPosition `json:"start"`
	End   CLIPosition `json:"end"`
}

// CLIToProtocol converts a 1-based UTF-8 position to a 0-based UTF-16
// protocol.Position suitable for passing to LSP methods.
//
// If the position is out of bounds, it is clamped to the nearest valid
// position (end of file or end of line) rather than returning an error.
// This is important because AI agents may hallucinate positions.
func CLIToProtocol(mapper *protocol.Mapper, pos CLIPosition) protocol.Position {
	content := mapper.Content

	// Build line start offsets.
	lineStarts := []int{0}
	for i, b := range content {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	totalLines := len(lineStarts)

	// Clamp line.
	line := min(max(pos.Line, 1), totalLines)

	lineStart := lineStarts[line-1]

	// Compute end of this line (byte offset of newline or EOF).
	var lineEnd int
	if line < totalLines {
		lineEnd = lineStarts[line] - 1 // exclude \n
		if lineEnd > lineStart && content[lineEnd-1] == '\r' {
			lineEnd--
		}
	} else {
		lineEnd = len(content)
	}
	lineLen := lineEnd - lineStart

	// Clamp column.
	col := max(pos.Column, 1)
	if col-1 > lineLen {
		col = lineLen + 1
	}

	offset := lineStart + (col - 1)

	ppos, err := mapper.OffsetPosition(offset)
	if err != nil {
		return protocol.Position{}
	}
	return ppos
}

// ProtocolToCLI converts a 0-based UTF-16 protocol.Position back to a
// 1-based UTF-8 CLIPosition.
func ProtocolToCLI(mapper *protocol.Mapper, pos protocol.Position) (CLIPosition, error) {
	offset, err := mapper.PositionOffset(pos)
	if err != nil {
		return CLIPosition{}, fmt.Errorf("PositionOffset: %w", err)
	}
	line, col8 := mapper.OffsetLineCol8(offset)
	return CLIPosition{Line: line, Column: col8}, nil
}

// LocationToCLI converts a protocol.Location to a CLILocation with
// 1-based UTF-8 positions.
func LocationToCLI(loc protocol.Location) CLILocation {
	uri := loc.URI
	path := uri.Path()
	if path == "" {
		path = string(uri)
	}
	// Note: we can't convert to UTF-8 byte columns without file content.
	// Return the protocol line/column (0-based → 1-based) as an approximation.
	// For exact conversion, callers should use ProtocolToCLI with a mapper.
	return CLILocation{
		File: path,
		Start: CLIPosition{
			Line:   int(loc.Range.Start.Line) + 1,
			Column: int(loc.Range.Start.Character) + 1,
		},
		End: CLIPosition{
			Line:   int(loc.Range.End.Line) + 1,
			Column: int(loc.Range.End.Character) + 1,
		},
	}
}

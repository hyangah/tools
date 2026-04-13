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
	Line   int // 1-based
	Column int // 1-based, UTF-8 byte offset from line start
}

// CLILocation converts a protocol.Location to a human-readable location
// with 1-based UTF-8 positions.
type CLILocation struct {
	File       string // absolute file path (from URI)
	Start, End CLIPosition
}

// CLIToProtocol converts a 1-based UTF-8 position to a 0-based UTF-16
// protocol.Position suitable for passing to golang.* functions.
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
	line := max(pos.Line, 1)
	if line > totalLines {
		line = totalLines
	}

	lineStart := lineStarts[line-1]

	// Compute end of this line (byte offset of newline or EOF).
	var lineEnd int
	if line < totalLines {
		lineEnd = lineStarts[line] - 1 // exclude \n
		// Also exclude \r if present (CRLF).
		if lineEnd > lineStart && content[lineEnd-1] == '\r' {
			lineEnd--
		}
	} else {
		lineEnd = len(content) // last line extends to EOF
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
		// Shouldn't happen after clamping, but be defensive.
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
func LocationToCLI(mapper *protocol.Mapper, loc protocol.Location) (CLILocation, error) {
	start, err := ProtocolToCLI(mapper, loc.Range.Start)
	if err != nil {
		return CLILocation{}, fmt.Errorf("start: %w", err)
	}
	end, err := ProtocolToCLI(mapper, loc.Range.End)
	if err != nil {
		return CLILocation{}, fmt.Errorf("end: %w", err)
	}

	uri := loc.URI
	path := uri.Path()
	if path == "" {
		path = string(uri)
	}

	return CLILocation{
		File:  path,
		Start: start,
		End:   end,
	}, nil
}

// FormatCLIPosition formats a CLIPosition as "line:col".
func FormatCLIPosition(p CLIPosition) string {
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// FormatCLILocation formats a CLILocation as "file:line:col" (start only)
// or "file:line:col-line:col" (if start != end).
func FormatCLILocation(loc CLILocation) string {
	if loc.Start == loc.End {
		return fmt.Sprintf("%s:%s", loc.File, FormatCLIPosition(loc.Start))
	}
	return fmt.Sprintf("%s:%s-%s", loc.File, FormatCLIPosition(loc.Start), FormatCLIPosition(loc.End))
}

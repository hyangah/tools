// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli_test

import (
	"testing"

	"golang.org/x/tools/gopls/internal/goplscli"
	"golang.org/x/tools/gopls/internal/protocol"
)

// makeMapper creates a Mapper for the given content with a dummy URI.
func makeMapper(content string) *protocol.Mapper {
	return protocol.NewMapper("file:///test.go", []byte(content))
}

// TestPositionRoundTripASCII tests round-trip conversion on an ASCII-only file.
func TestPositionRoundTripASCII(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	mapper := makeMapper(content)

	tests := []goplscli.CLIPosition{
		{Line: 1, Column: 1}, // start of file
		{Line: 1, Column: 8}, // "m" in "main"
		{Line: 3, Column: 6}, // "m" in second "main"
		{Line: 4, Column: 2}, // "f" in "fmt" (after tab)
		{Line: 5, Column: 1}, // "}"
	}

	for _, pos := range tests {
		ppos := goplscli.CLIToProtocol(mapper, pos)
		got, err := goplscli.ProtocolToCLI(mapper, ppos)
		if err != nil {
			t.Errorf("ProtocolToCLI(%v) error: %v", ppos, err)
			continue
		}
		if got != pos {
			t.Errorf("round-trip %v: CLIToProtocol=%v, back=%v", pos, ppos, got)
		}
	}
}

// TestPositionRoundTripUnicode tests conversion with multi-byte UTF-8
// characters where UTF-8 byte columns differ from UTF-16 code units.
func TestPositionRoundTripUnicode(t *testing.T) {
	// "Ünïcödé" in UTF-8:
	//   Ü = 2 bytes (U+00DC), n = 1, ï = 2 (U+00EF), c = 1,
	//   ö = 2 (U+00F6), d = 1, é = 2 (U+00E9) = 11 bytes total, 7 UTF-16 code units
	content := "package main\n\nfunc Ünïcödé() string {\n\treturn \"héllo\"\n}\n"
	mapper := makeMapper(content)

	// "Ü" starts after "func " (5 bytes) on line 3.
	// 1-based UTF-8 column = 6.
	cli := goplscli.CLIPosition{Line: 3, Column: 6}
	ppos := goplscli.CLIToProtocol(mapper, cli)

	// 0-based line should be 2.
	if ppos.Line != 2 {
		t.Errorf("expected protocol line 2, got %d", ppos.Line)
	}
	// "func " is 5 ASCII chars, so UTF-16 character should be 5.
	if ppos.Character != 5 {
		t.Errorf("expected protocol character 5, got %d", ppos.Character)
	}

	// Round-trip back.
	got, err := goplscli.ProtocolToCLI(mapper, ppos)
	if err != nil {
		t.Fatalf("ProtocolToCLI error: %v", err)
	}
	if got != cli {
		t.Errorf("round-trip: expected %v, got %v", cli, got)
	}

	// Test a position after multi-byte chars: the "(" after "Ünïcödé".
	// "func " = 5 bytes, "Ünïcödé" = 11 bytes, so "(" is at byte 17 from line start.
	// 1-based UTF-8 column = 17.
	cliParen := goplscli.CLIPosition{Line: 3, Column: 17}
	pposParen := goplscli.CLIToProtocol(mapper, cliParen)

	// UTF-16: "func " = 5, "Ünïcödé" = 7, so "(" is at UTF-16 offset 12.
	if pposParen.Character != 12 {
		t.Errorf("paren: expected protocol character 12, got %d", pposParen.Character)
	}

	gotParen, err := goplscli.ProtocolToCLI(mapper, pposParen)
	if err != nil {
		t.Fatalf("ProtocolToCLI paren error: %v", err)
	}
	if gotParen != cliParen {
		t.Errorf("paren round-trip: expected %v, got %v", cliParen, gotParen)
	}
}

// TestPositionClampLineBeyondEOF tests that line numbers beyond EOF are clamped.
func TestPositionClampLineBeyondEOF(t *testing.T) {
	content := "line1\nline2\nline3\n"
	mapper := makeMapper(content)

	// Line 100 should be clamped to the last line (line 4, which is empty after trailing \n).
	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 100, Column: 1})

	// The content has 3 lines with text + 1 empty line after trailing \n = 4 lines.
	// Last line (line 4, 0-based 3) is empty.
	if pos.Line != 3 {
		t.Errorf("clamped line: expected 3 (0-based), got %d", pos.Line)
	}
	if pos.Character != 0 {
		t.Errorf("clamped char: expected 0, got %d", pos.Character)
	}
}

// TestPositionClampColumnBeyondEOL tests that columns beyond end-of-line are clamped.
func TestPositionClampColumnBeyondEOL(t *testing.T) {
	content := "short\nmedium line\n"
	mapper := makeMapper(content)

	// "short" is 5 chars. Column 100 should clamp to column 6 (one past last char).
	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 1, Column: 100})

	if pos.Line != 0 {
		t.Errorf("expected line 0, got %d", pos.Line)
	}
	// "short" = 5 bytes, clamped column = 6, so offset is 5, UTF-16 char = 5.
	if pos.Character != 5 {
		t.Errorf("expected character 5, got %d", pos.Character)
	}
}

// TestPositionStartOfFile tests position at start of file (1:1).
func TestPositionStartOfFile(t *testing.T) {
	content := "package main\n"
	mapper := makeMapper(content)

	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 1, Column: 1})
	if pos.Line != 0 || pos.Character != 0 {
		t.Errorf("start of file: expected (0,0), got (%d,%d)", pos.Line, pos.Character)
	}

	cli, err := goplscli.ProtocolToCLI(mapper, pos)
	if err != nil {
		t.Fatal(err)
	}
	if cli.Line != 1 || cli.Column != 1 {
		t.Errorf("start of file round-trip: expected (1,1), got (%d,%d)", cli.Line, cli.Column)
	}
}

// TestPositionEndOfFile tests position at end of file.
func TestPositionEndOfFile(t *testing.T) {
	content := "ab\ncd"
	mapper := makeMapper(content)

	// End of file: line 2, column 3 (one past "d").
	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 2, Column: 3})
	if pos.Line != 1 || pos.Character != 2 {
		t.Errorf("end of file: expected (1,2), got (%d,%d)", pos.Line, pos.Character)
	}
}

// TestPositionEmptyLine tests position on an empty line.
func TestPositionEmptyLine(t *testing.T) {
	content := "line1\n\nline3\n"
	mapper := makeMapper(content)

	// Line 2 is empty. Column 1 should give position at start of empty line.
	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 2, Column: 1})
	if pos.Line != 1 {
		t.Errorf("empty line: expected line 1, got %d", pos.Line)
	}
	if pos.Character != 0 {
		t.Errorf("empty line: expected character 0, got %d", pos.Character)
	}

	// Column beyond empty line should clamp.
	pos2 := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 2, Column: 50})
	if pos2.Line != 1 || pos2.Character != 0 {
		t.Errorf("empty line clamped: expected (1,0), got (%d,%d)", pos2.Line, pos2.Character)
	}
}

// TestPositionClampZeroValues tests that zero/negative line and column are clamped.
func TestPositionClampZeroValues(t *testing.T) {
	content := "hello\n"
	mapper := makeMapper(content)

	pos := goplscli.CLIToProtocol(mapper, goplscli.CLIPosition{Line: 0, Column: 0})
	if pos.Line != 0 || pos.Character != 0 {
		t.Errorf("zero values: expected (0,0), got (%d,%d)", pos.Line, pos.Character)
	}
}

// TestLocationToCLI tests converting a protocol.Location to a CLILocation.
func TestLocationToCLI(t *testing.T) {
	content := "package main\n\nfunc Hello() {}\n"
	uri := protocol.DocumentURI("file:///tmp/test.go")
	mapper := protocol.NewMapper(uri, []byte(content))

	loc := protocol.Location{
		URI: uri,
		Range: protocol.Range{
			Start: protocol.Position{Line: 2, Character: 5},  // "H" in "Hello"
			End:   protocol.Position{Line: 2, Character: 10}, // end of "Hello"
		},
	}

	cliLoc, err := goplscli.LocationToCLI(mapper, loc)
	if err != nil {
		t.Fatalf("LocationToCLI error: %v", err)
	}

	if cliLoc.File != "/tmp/test.go" {
		t.Errorf("expected file /tmp/test.go, got %s", cliLoc.File)
	}
	// "func " = 5 bytes. "H" is at 1-based column 6.
	if cliLoc.Start.Line != 3 || cliLoc.Start.Column != 6 {
		t.Errorf("start: expected (3,6), got (%d,%d)", cliLoc.Start.Line, cliLoc.Start.Column)
	}
	// "Hello" ends at column 10+1=11 (1-based).
	if cliLoc.End.Line != 3 || cliLoc.End.Column != 11 {
		t.Errorf("end: expected (3,11), got (%d,%d)", cliLoc.End.Line, cliLoc.End.Column)
	}
}

// TestFormatCLILocation tests the formatting helpers.
func TestFormatCLILocation(t *testing.T) {
	loc := goplscli.CLILocation{
		File:  "/tmp/test.go",
		Start: goplscli.CLIPosition{Line: 10, Column: 5},
		End:   goplscli.CLIPosition{Line: 10, Column: 15},
	}
	got := goplscli.FormatCLILocation(loc)
	want := "/tmp/test.go:10:5-10:15"
	if got != want {
		t.Errorf("FormatCLILocation: got %q, want %q", got, want)
	}

	// Same start and end.
	loc.End = loc.Start
	got = goplscli.FormatCLILocation(loc)
	want = "/tmp/test.go:10:5"
	if got != want {
		t.Errorf("FormatCLILocation (point): got %q, want %q", got, want)
	}
}

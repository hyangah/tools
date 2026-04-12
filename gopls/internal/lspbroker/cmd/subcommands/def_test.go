// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"testing"
)

func TestParseDefArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantSymbol string
		wantIn     string
		wantErr    bool
	}{
		{
			name:    "missing --in",
			args:    []string{"Parse"},
			wantErr: true,
		},
		{
			name:       "symbol + --in file",
			args:       []string{"Parse", "--in", "./parser.go"},
			wantSymbol: "Parse",
			wantIn:     "./parser.go",
		},
		{
			name:       "symbol + --in file:line",
			args:       []string{"Parse", "--in", "./parser.go:42"},
			wantSymbol: "Parse",
			wantIn:     "./parser.go:42",
		},
		{
			name:   "positional bypass --in file:line:col",
			args:   []string{"--in", "./parser.go:42:10"},
			wantIn: "./parser.go:42:10",
		},
		{
			name:       "--in=value form",
			args:       []string{"Parse", "--in=./parser.go"},
			wantSymbol: "Parse",
			wantIn:     "./parser.go",
		},
		{
			name:    "too many positional args",
			args:    []string{"one", "two", "--in", "file.go"},
			wantErr: true,
		},
		{
			name:    "--in without value",
			args:    []string{"--in"},
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"--foo", "--in", "file.go"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			symbol, inVal, err := parseDefArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if symbol != tt.wantSymbol {
				t.Errorf("symbol = %q, want %q", symbol, tt.wantSymbol)
			}
			if inVal != tt.wantIn {
				t.Errorf("inValue = %q, want %q", inVal, tt.wantIn)
			}
		})
	}
}

func TestParseInValue(t *testing.T) {
	tests := []struct {
		input    string
		wantFile string
		wantLine int
		wantCol  int
	}{
		{"./parser.go", "./parser.go", 0, 0},
		{"./parser.go:42", "./parser.go", 42, 0},
		{"./parser.go:42:10", "./parser.go", 42, 10},
		{"/abs/path/to/file.go:1:1", "/abs/path/to/file.go", 1, 1},
		// Paths with colons (e.g. no numeric suffix).
		{"file.go:notanumber", "file.go:notanumber", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			file, line, col, err := parseInValue(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if file != tt.wantFile {
				t.Errorf("file = %q, want %q", file, tt.wantFile)
			}
			if line != tt.wantLine {
				t.Errorf("line = %d, want %d", line, tt.wantLine)
			}
			if col != tt.wantCol {
				t.Errorf("col = %d, want %d", col, tt.wantCol)
			}
		})
	}
}

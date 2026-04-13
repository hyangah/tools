// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/tools/gopls/internal/lspbroker"
	"golang.org/x/tools/gopls/internal/lspbroker/format"
	"golang.org/x/tools/gopls/internal/protocol"
)

// DiagnosticsFlags holds the parsed flags for the "diagnostics" subcommand.
type DiagnosticsFlags struct {
	// JSON requests --json output instead of formatted text.
	JSON bool

	// Project requests diagnostics for all files in the project.
	Project bool

	// NoSpawn prevents daemon auto-spawn.
	NoSpawn bool

	// Timeout is the wall-clock limit for the whole invocation.
	Timeout time.Duration
}

// RunDiagnostics implements `gopls lspcli diagnostics [FILE | --project]`.
//
// Without --project: returns diagnostics for the given FILE.
// With --project: returns diagnostics for all tracked files.
//
// Exit codes:
//
//	0  success, possibly empty result
//	2  user error (bad args)
//	3  LSP error
//	4  broker error
func RunDiagnostics(ctx context.Context, args []string, flags DiagnosticsFlags, cacheDir, selfPath string, w io.Writer) (exitCode int, err error) {
	var file string

	// Parse args: optional positional FILE, plus --project flag.
	for _, a := range args {
		switch {
		case a == "--project":
			flags.Project = true
		case a == "--json":
			flags.JSON = true
		default:
			if file != "" {
				return 2, fmt.Errorf("too many arguments; expected at most one FILE")
			}
			file = a
		}
	}

	if file == "" && !flags.Project {
		return 2, fmt.Errorf("error: FILE or --project is required\n  usage: diagnostics FILE\n  usage: diagnostics --project")
	}
	if file != "" && flags.Project {
		return 2, fmt.Errorf("error: FILE and --project are mutually exclusive")
	}

	// Resolve file to absolute path if provided.
	var absFile string
	if file != "" {
		var err error
		absFile, err = resolveFile(file)
		if err != nil {
			return 2, err
		}
	}

	timeout := flags.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if !flags.NoSpawn {
		if err := EnsureDaemonRunning(ctx, cacheDir, selfPath); err != nil {
			return 4, fmt.Errorf("start broker daemon: %w", err)
		}
	}

	bc, err := DialBroker(ctx, cacheDir, selfPath)
	if err != nil {
		if flags.NoSpawn {
			return 4, fmt.Errorf("broker not running (--no-spawn): %w", err)
		}
		return 4, fmt.Errorf("connect to broker: %w", err)
	}
	defer bc.Close()

	params := lspbroker.DiagnosticsParams{
		Version: lspbroker.ProtocolVersion,
		Project: flags.Project,
		File:    absFile,
	}

	raw, err := bc.Diagnostics(ctx, params)
	if err != nil {
		return classifyError(err), err
	}

	if flags.Project {
		// Decode as map[string][]protocol.Diagnostic
		var all map[string][]protocol.Diagnostic
		if err := json.Unmarshal(raw, &all); err != nil {
			return 3, fmt.Errorf("decode project diagnostics: %w", err)
		}
		format.FormatProjectDiagnostics(w, all, flags.JSON)
	} else {
		// Decode as []protocol.Diagnostic
		var diags []protocol.Diagnostic
		if err := json.Unmarshal(raw, &diags); err != nil {
			return 3, fmt.Errorf("decode file diagnostics: %w", err)
		}
		uri := string(protocol.URIFromPath(absFile))
		format.FormatDiagnostics(w, uri, diags, flags.JSON)
		if len(diags) == 0 {
			fmt.Fprintln(os.Stderr, "note: no diagnostics found. If this is a fresh session, gopls may still be loading. Retry in a few seconds.")
		}
	}

	return 0, nil
}

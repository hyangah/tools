// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subcommands

import (
	"context"
	"fmt"
	"path/filepath"

	"golang.org/x/tools/gopls/internal/lspbroker"
)

// RunTrust dispatches the trust subcommand: add, remove, list.
//
// Usage:
//
//	gopls lspcli trust add [PATH]    — trust PATH (default: cwd)
//	gopls lspcli trust remove [PATH] — remove PATH from trust list (default: cwd)
//	gopls lspcli trust list          — list trusted paths
func RunTrust(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("trust: must provide action (add|remove|list)")
	}
	action := args[0]
	rest := args[1:]

	storePath := lspbroker.DefaultTrustStorePath()
	ts, err := lspbroker.LoadTrustStore(storePath)
	if err != nil {
		return fmt.Errorf("trust: load trust store: %w", err)
	}

	switch action {
	case "add":
		path, err := resolvePathArg(rest)
		if err != nil {
			return fmt.Errorf("trust add: %w", err)
		}
		if err := ts.Add(path); err != nil {
			return fmt.Errorf("trust add: %w", err)
		}
		fmt.Printf("trusted: %s\n", path)
		return nil

	case "remove":
		path, err := resolvePathArg(rest)
		if err != nil {
			return fmt.Errorf("trust remove: %w", err)
		}
		if err := ts.Remove(path); err != nil {
			return fmt.Errorf("trust remove: %w", err)
		}
		fmt.Printf("removed: %s\n", path)
		return nil

	case "list":
		for _, p := range ts.List() {
			fmt.Println(p)
		}
		return nil

	default:
		return fmt.Errorf("trust: unknown action %q (want add|remove|list)", action)
	}
}

// resolvePathArg returns an absolute path from the first element of args,
// or the current working directory if args is empty.
func resolvePathArg(args []string) (string, error) {
	var raw string
	if len(args) > 0 {
		raw = args[0]
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return abs, nil
}

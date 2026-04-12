// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// TrustStore manages the set of project root directories whose .lsp.json
// files are allowed to spawn LSP server subprocesses.
//
// The trust list is persisted at $XDG_CONFIG_HOME/lsp-broker/trusted.json
// (falling back to ~/.config/lsp-broker/trusted.json). Format:
//
//	{"paths": ["/abs/path/one", "/abs/path/two"]}
//
// Paths are matched as prefixes: trusting "/a/b" also trusts "/a/b/c/d".
type TrustStore struct {
	path  string   // absolute path to trusted.json
	paths []string // trusted root prefixes
}

// trustStoreJSON is the on-disk format.
type trustStoreJSON struct {
	Paths []string `json:"paths"`
}

// DefaultTrustStorePath returns the default path for trusted.json:
// $XDG_CONFIG_HOME/lsp-broker/trusted.json, or ~/.config/lsp-broker/trusted.json.
func DefaultTrustStorePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		// Fallback: use home directory.
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "lsp-broker", "trusted.json")
}

// LoadTrustStore reads the trust store from the given path.
// If the file doesn't exist, returns an empty (but usable) TrustStore.
// The path is remembered for subsequent Add/Remove calls.
func LoadTrustStore(path string) (*TrustStore, error) {
	ts := &TrustStore{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ts, nil
	}
	if err != nil {
		return nil, err
	}
	var j trustStoreJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	for _, p := range j.Paths {
		ts.paths = append(ts.paths, filepath.Clean(p))
	}
	return ts, nil
}

// IsTrusted reports whether root (an absolute path) is in the trust list.
// A path is trusted if any entry in the trust list is a prefix of root
// (on path boundaries).
func (ts *TrustStore) IsTrusted(root string) bool {
	root = filepath.Clean(root)
	for _, prefix := range ts.paths {
		if root == prefix {
			return true
		}
		if strings.HasPrefix(root, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Add adds root to the trust list and persists to disk.
// root must be an absolute path. Duplicates are ignored.
func (ts *TrustStore) Add(root string) error {
	root = filepath.Clean(root)
	if slices.Contains(ts.paths, root) {
		return nil // already present
	}
	ts.paths = append(ts.paths, root)
	return ts.save()
}

// Remove removes root from the trust list and persists to disk.
// It is not an error if root was not in the list.
func (ts *TrustStore) Remove(root string) error {
	root = filepath.Clean(root)
	filtered := make([]string, 0, len(ts.paths))
	for _, p := range ts.paths {
		if p != root {
			filtered = append(filtered, p)
		}
	}
	ts.paths = filtered
	return ts.save()
}

// List returns a copy of all trusted paths.
func (ts *TrustStore) List() []string {
	result := make([]string, len(ts.paths))
	copy(result, ts.paths)
	return result
}

// save writes the trust store to disk.
func (ts *TrustStore) save() error {
	if err := os.MkdirAll(filepath.Dir(ts.path), 0700); err != nil {
		return err
	}
	j := trustStoreJSON{Paths: ts.paths}
	if j.Paths == nil {
		j.Paths = []string{}
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return os.WriteFile(ts.path, data, 0600)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig writes content to .lsp.json in dir.
func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".lsp.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	const cfg = `{
		"version": 1,
		"servers": {
			"typescript": {
				"command": ["typescript-language-server", "--stdio"],
				"extensionToLanguage": {
					".ts": "typescript",
					".tsx": "typescriptreact"
				},
				"env": {
					"NODE_PATH": "${WORKSPACE_ROOT}/node_modules"
				},
				"initializationOptions": {"preferences": {"includeCompletionsForModuleExports": false}},
				"settings": {"typescript": {"format": {"enable": false}}},
				"workspaceFolder": "${WORKSPACE_ROOT}/web",
				"startupTimeout": "30s",
				"maxRestarts": 3
			}
		}
	}`
	writeConfig(t, dir, cfg)

	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c == nil {
		t.Fatal("LoadConfig returned nil config")
	}
	if c.Version != 1 {
		t.Errorf("Version = %d, want 1", c.Version)
	}
	if c.root != dir {
		t.Errorf("root = %q, want %q", c.root, dir)
	}

	srv, ok := c.Servers["typescript"]
	if !ok {
		t.Fatal("server 'typescript' not found")
	}
	if len(srv.Command) != 2 || srv.Command[0] != "typescript-language-server" {
		t.Errorf("Command = %v, want [typescript-language-server --stdio]", srv.Command)
	}
	if srv.ExtensionToLanguage[".ts"] != "typescript" {
		t.Errorf(".ts language = %q, want 'typescript'", srv.ExtensionToLanguage[".ts"])
	}
	if srv.ExtensionToLanguage[".tsx"] != "typescriptreact" {
		t.Errorf(".tsx language = %q, want 'typescriptreact'", srv.ExtensionToLanguage[".tsx"])
	}
	wantNodePath := dir + "/node_modules"
	if srv.Env["NODE_PATH"] != wantNodePath {
		t.Errorf("NODE_PATH = %q, want %q", srv.Env["NODE_PATH"], wantNodePath)
	}
	wantWorkspace := dir + "/web"
	if srv.WorkspaceFolder != wantWorkspace {
		t.Errorf("WorkspaceFolder = %q, want %q", srv.WorkspaceFolder, wantWorkspace)
	}
	if srv.startupDuration != 30*time.Second {
		t.Errorf("startupDuration = %v, want 30s", srv.startupDuration)
	}
	if srv.MaxRestarts != 3 {
		t.Errorf("MaxRestarts = %d, want 3", srv.MaxRestarts)
	}

	// extensionRouting built correctly.
	id, sc := c.ServerForExtension(".ts")
	if id != "typescript" || sc == nil {
		t.Errorf("ServerForExtension('.ts') = (%q, %v), want ('typescript', non-nil)", id, sc)
	}
	id, sc = c.ServerForExtension(".tsx")
	if id != "typescript" || sc == nil {
		t.Errorf("ServerForExtension('.tsx') = (%q, %v), want ('typescript', non-nil)", id, sc)
	}
	id, sc = c.ServerForExtension(".go")
	if id != "" || sc != nil {
		t.Errorf("ServerForExtension('.go') = (%q, %v), want ('', nil)", id, sc)
	}

	// initializationOptions and settings round-trip.
	if srv.InitializationOptions == nil {
		t.Error("InitializationOptions is nil")
	}
	var initOpts map[string]any
	if err := json.Unmarshal(srv.InitializationOptions, &initOpts); err != nil {
		t.Errorf("InitializationOptions unmarshal: %v", err)
	}
	if srv.Settings == nil {
		t.Error("Settings is nil")
	}
}

func TestLoadConfig_NoFile(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c != nil {
		t.Errorf("LoadConfig returned non-nil config for missing file")
	}
}

func TestLoadConfig_BadVersion(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"version": 2, "servers": {"x": {"command": ["x"], "extensionToLanguage": {".x": "x"}}}}`)
	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("LoadConfig: expected error for version 2, got nil")
	}
}

func TestLoadConfig_EmptyServers(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"version": 1, "servers": {}}`)
	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("LoadConfig: expected error for empty servers, got nil")
	}
}

func TestLoadConfig_EmptyCommand(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"version": 1, "servers": {"x": {"command": [], "extensionToLanguage": {".x": "x"}}}}`)
	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("LoadConfig: expected error for empty command, got nil")
	}
}

func TestLoadConfig_NoExtensions(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"version": 1, "servers": {"x": {"command": ["x"], "extensionToLanguage": {}}}}`)
	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("LoadConfig: expected error for empty extensionToLanguage, got nil")
	}
}

func TestLoadConfig_DuplicateExtensions(t *testing.T) {
	dir := t.TempDir()
	// Both servers claim .ts; first declaration (alphabetical by JSON key) wins.
	// We use "aserver" and "bserver" to have a stable ordering.
	writeConfig(t, dir, `{
		"version": 1,
		"servers": {
			"aserver": {
				"command": ["a"],
				"extensionToLanguage": {".ts": "typescript"}
			},
			"bserver": {
				"command": ["b"],
				"extensionToLanguage": {".ts": "typescript2"}
			}
		}
	}`)
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("LoadConfig returned nil config")
	}
	// One of the servers should own .ts; no error expected.
	id, sc := c.ServerForExtension(".ts")
	if id == "" || sc == nil {
		t.Error("ServerForExtension('.ts') should return a server")
	}
}

func TestLoadConfig_VariableExpansion(t *testing.T) {
	dir := t.TempDir()
	// Set a test env var.
	t.Setenv("TEST_LSP_VAR", "myvalue")

	writeConfig(t, dir, `{
		"version": 1,
		"servers": {
			"test": {
				"command": ["server", "${WORKSPACE_ROOT}/bin/server"],
				"extensionToLanguage": {".x": "x"},
				"env": {
					"HOME_VAR": "${HOME}/config",
					"ENV_VAR": "${TEST_LSP_VAR}/path",
					"UNKNOWN_VAR": "${UNKNOWN_LSPBROKER_VAR}/path"
				},
				"workspaceFolder": "${WORKSPACE_ROOT}/src"
			}
		}
	}`)

	home, _ := os.UserHomeDir()

	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := c.Servers["test"]

	// WORKSPACE_ROOT expansion in command.
	wantCmd := dir + "/bin/server"
	if srv.Command[1] != wantCmd {
		t.Errorf("Command[1] = %q, want %q", srv.Command[1], wantCmd)
	}

	// HOME expansion.
	wantHome := home + "/config"
	if srv.Env["HOME_VAR"] != wantHome {
		t.Errorf("HOME_VAR = %q, want %q", srv.Env["HOME_VAR"], wantHome)
	}

	// Env var expansion.
	wantEnv := "myvalue/path"
	if srv.Env["ENV_VAR"] != wantEnv {
		t.Errorf("ENV_VAR = %q, want %q", srv.Env["ENV_VAR"], wantEnv)
	}

	// Unknown var: left as-is.
	wantUnknown := "${UNKNOWN_LSPBROKER_VAR}/path"
	if srv.Env["UNKNOWN_VAR"] != wantUnknown {
		t.Errorf("UNKNOWN_VAR = %q, want %q", srv.Env["UNKNOWN_VAR"], wantUnknown)
	}

	// WorkspaceFolder expansion.
	wantWS := dir + "/src"
	if srv.WorkspaceFolder != wantWS {
		t.Errorf("WorkspaceFolder = %q, want %q", srv.WorkspaceFolder, wantWS)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	// Omit startupTimeout and maxRestarts.
	writeConfig(t, dir, `{
		"version": 1,
		"servers": {
			"x": {
				"command": ["x"],
				"extensionToLanguage": {".x": "x"}
			}
		}
	}`)
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := c.Servers["x"]
	if srv.startupDuration != 30*time.Second {
		t.Errorf("startupDuration = %v, want 30s", srv.startupDuration)
	}
	if srv.MaxRestarts != 3 {
		t.Errorf("MaxRestarts = %d, want 3", srv.MaxRestarts)
	}
}

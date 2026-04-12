// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the parsed representation of a .lsp.json file.
type Config struct {
	Version int                      `json:"version"`
	Servers map[string]*ServerConfig `json:"servers"`

	// Derived at load time, not serialized:
	extensionRouting map[string]string // file extension (e.g. ".ts") → server ID
	root             string            // absolute path to project root where .lsp.json lives
}

// ServerConfig describes one LSP server entry in .lsp.json.
type ServerConfig struct {
	Command               []string          `json:"command"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	Env                   map[string]string `json:"env,omitempty"`
	InitializationOptions json.RawMessage   `json:"initializationOptions,omitempty"`
	Settings              json.RawMessage   `json:"settings,omitempty"`
	WorkspaceFolder       string            `json:"workspaceFolder,omitempty"`
	StartupTimeout        string            `json:"startupTimeout,omitempty"` // parsed as time.Duration
	MaxRestarts           int               `json:"maxRestarts,omitempty"`

	// Derived:
	startupDuration time.Duration
}

// LoadConfig reads and parses .lsp.json from the given project root directory.
// Returns nil, nil if no .lsp.json exists at root.
// Returns an error if the file exists but is invalid.
func LoadConfig(root string) (*Config, error) {
	path := filepath.Join(root, ".lsp.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lspbroker: reading %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("lspbroker: parsing %s: %w", path, err)
	}

	if cfg.Version != 1 {
		return nil, fmt.Errorf("lspbroker: %s: unsupported version %d (want 1)", path, cfg.Version)
	}
	if len(cfg.Servers) == 0 {
		return nil, fmt.Errorf("lspbroker: %s: servers must be non-empty", path)
	}

	cfg.root = root
	cfg.extensionRouting = make(map[string]string)

	home, _ := os.UserHomeDir()
	expander := func(s string) string {
		return expandVars(s, root, home)
	}

	for id, srv := range cfg.Servers {
		if len(srv.Command) == 0 {
			return nil, fmt.Errorf("lspbroker: %s: server %q: command must be non-empty", path, id)
		}
		if len(srv.ExtensionToLanguage) == 0 {
			return nil, fmt.Errorf("lspbroker: %s: server %q: extensionToLanguage must be non-empty", path, id)
		}

		// Apply variable expansion to command args.
		for i, arg := range srv.Command {
			srv.Command[i] = expander(arg)
		}

		// Apply variable expansion to env values.
		for k, v := range srv.Env {
			srv.Env[k] = expander(v)
		}

		// Apply variable expansion to workspaceFolder.
		if srv.WorkspaceFolder != "" {
			srv.WorkspaceFolder = expander(srv.WorkspaceFolder)
		}

		// Parse startupTimeout.
		if srv.StartupTimeout != "" {
			d, err := time.ParseDuration(srv.StartupTimeout)
			if err != nil {
				return nil, fmt.Errorf("lspbroker: %s: server %q: invalid startupTimeout %q: %w", path, id, srv.StartupTimeout, err)
			}
			srv.startupDuration = d
		} else {
			srv.startupDuration = 30 * time.Second
		}

		// Default maxRestarts.
		if srv.MaxRestarts == 0 {
			srv.MaxRestarts = 3
		}

		// Build extension routing, first declaration wins.
		for ext := range srv.ExtensionToLanguage {
			if existing, ok := cfg.extensionRouting[ext]; ok {
				log.Printf("lspbroker: %s: extension %q claimed by both %q and %q; keeping %q", path, ext, existing, id, existing)
				continue
			}
			cfg.extensionRouting[ext] = id
		}
	}

	return &cfg, nil
}

// ServerForExtension returns the server ID and config for a file extension,
// or ("", nil) if no server handles that extension.
func (c *Config) ServerForExtension(ext string) (string, *ServerConfig) {
	if c == nil {
		return "", nil
	}
	id, ok := c.extensionRouting[ext]
	if !ok {
		return "", nil
	}
	return id, c.Servers[id]
}

// expandVars replaces known variable references in s:
//   - ${WORKSPACE_ROOT} → root
//   - ${HOME} → home (from os.UserHomeDir)
//   - ${OTHER} → os.Getenv("OTHER")
//
// Unknown or empty variables are left as-is and a warning is logged.
func expandVars(s, root, home string) string {
	return varReplace(s, root, home)
}

func varReplace(s, root, home string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, "${")
		if start == -1 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		s = s[start:]
		end := strings.Index(s, "}")
		if end == -1 {
			// No closing brace — leave the rest as-is.
			b.WriteString(s)
			break
		}
		varName := s[2:end] // strip ${ and }
		s = s[end+1:]

		switch varName {
		case "WORKSPACE_ROOT":
			b.WriteString(root)
		case "HOME":
			if home == "" {
				log.Printf("lspbroker: variable ${HOME}: os.UserHomeDir() returned empty; leaving as-is")
				b.WriteString("${HOME}")
			} else {
				b.WriteString(home)
			}
		default:
			val := os.Getenv(varName)
			if val == "" {
				log.Printf("lspbroker: variable ${%s}: not set or empty; leaving as-is", varName)
				b.WriteString("${")
				b.WriteString(varName)
				b.WriteString("}")
			} else {
				b.WriteString(val)
			}
		}
	}
	return b.String()
}

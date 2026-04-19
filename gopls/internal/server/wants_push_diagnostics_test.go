// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// TestInitializeWantsPushDiagnostics asserts that the wantsPushDiagnostics
// key in initializationOptions is read during Initialize and reflected on
// the *server field that gates pool subscription and push publication.
func TestInitializeWantsPushDiagnostics(t *testing.T) {
	// Create an empty module on disk so Initialize has a real RootURI.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fake\n\ngo 1.18\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts map[string]any
		want bool
	}{
		{"default (absent)", nil, true},
		{"explicit true", map[string]any{"wantsPushDiagnostics": true}, true},
		{"explicit false (CLI profile)", map[string]any{"wantsPushDiagnostics": false}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			session := cache.NewSession(ctx, cache.New(nil))
			srv := New(session, nopClient{}, settings.DefaultOptions()).(*server)

			params := &protocol.ParamInitialize{}
			params.RootURI = protocol.URIFromPath(tmp)
			params.Capabilities.Workspace.Configuration = true
			if tc.opts != nil {
				params.InitializationOptions = tc.opts
			}
			if _, err := srv.Initialize(ctx, params); err != nil {
				t.Fatal(err)
			}
			if got := srv.wantsPushDiagnostics; got != tc.want {
				t.Errorf("wantsPushDiagnostics = %v, want %v", got, tc.want)
			}
		})
	}
}

// nopClient is a minimal protocol.ClientCloser used only to construct a
// *server for Initialize-only tests. Any callback the server makes during
// Initialize must be a no-op or return nil; this type is only safe for
// tests that don't exercise paths requiring a working client.
type nopClient struct{ protocol.Client }

func (nopClient) Close() error { return nil }

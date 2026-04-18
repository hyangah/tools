// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"testing"

	"golang.org/x/tools/gopls/internal/settings"
)

// TestInitParamsProfile asserts that the capability profile passed to
// initParams shapes the resulting ParamInitialize: the CLI profile
// drops WorkDoneProgress and advertises wantsPushDiagnostics=false via
// initializationOptions (the wire used by the server to populate
// s.wantsPushDiagnostics — see proposal §3.1, §4).
func TestInitParamsProfile(t *testing.T) {
	opts := settings.DefaultOptions()

	tests := []struct {
		name              string
		profile           clientProfile
		wantWorkDone      bool
		wantWantsPushDiag bool
	}{
		{"default", defaultClientProfile, true, true},
		{"cli", cliClientProfile, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := initParams("/tmp/fake", opts, tc.profile)
			if got := p.Capabilities.Window.WorkDoneProgress; got != tc.wantWorkDone {
				t.Errorf("WorkDoneProgress = %v, want %v", got, tc.wantWorkDone)
			}
			m, ok := p.InitializationOptions.(map[string]any)
			if !ok {
				t.Fatalf("InitializationOptions = %T, want map[string]any", p.InitializationOptions)
			}
			got, ok := m["wantsPushDiagnostics"].(bool)
			if !ok {
				t.Fatalf("InitializationOptions[wantsPushDiagnostics] = %v (%T), want bool", m["wantsPushDiagnostics"], m["wantsPushDiagnostics"])
			}
			if got != tc.wantWantsPushDiag {
				t.Errorf("wantsPushDiagnostics = %v, want %v", got, tc.wantWantsPushDiag)
			}
		})
	}
}

// TestCLIProfileSkipsDidOpen pins the CLI profile's client-side choice
// to skip DidOpen priming. The cliServer wrapper (cmd/cli.go) consults
// this bit to decide whether to open files before each query. Changing
// this default is a user-visible behavior change — prior sessions have
// toggled it back and forth — so a cheap pin guards against accidental
// flips.
func TestCLIProfileSkipsDidOpen(t *testing.T) {
	if !cliClientProfile.skipDidOpen {
		t.Error("cliClientProfile.skipDidOpen = false, want true (Stage 4b)")
	}
	if defaultClientProfile.skipDidOpen {
		t.Error("defaultClientProfile.skipDidOpen = true, want false")
	}
}

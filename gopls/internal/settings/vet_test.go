// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package settings_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/tools/gopls/internal/doc"
	"golang.org/x/tools/gopls/internal/settings"
	"golang.org/x/tools/internal/testenv"
)

// TestVetSuite ensures that gopls's analyser suite is a superset of vet's.
//
// This test may fail spuriously if gopls/doc/generate.TestGenerated
// fails. In that case retry after re-running the JSON generator.
func TestVetSuite(t *testing.T) {
	testenv.NeedsTool(t, "go")

	// Read gopls' suite from the API JSON.
	goplsAnalyzers := make(map[string]bool)
	var api doc.API
	if err := json.Unmarshal([]byte(doc.JSON), &api); err != nil {
		t.Fatal(err)
	}
	for _, a := range api.Analyzers {
		goplsAnalyzers[a.Name] = true
	}

	// Read vet's suite by parsing its help message.
	cmd := exec.Command("go", "tool", "vet", "help")
	cmd.Stdout = new(strings.Builder)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run vet: %v", err)
	}
	out := fmt.Sprint(cmd.Stdout)
	_, out, _ = strings.Cut(out, "Registered analyzers:\n\n")
	out, _, _ = strings.Cut(out, "\n\n")
	vetNames := make(map[string]bool)
	for line := range strings.SplitSeq(out, "\n") {
		name := strings.Fields(line)[0]
		if !goplsAnalyzers[name] {
			t.Errorf("gopls lacks vet analyzer %q", name)
		}
		vetNames[name] = true
	}

	// The inVet classification drives `gopls cli vet`'s filter. If a
	// cmd/vet analyzer isn't marked inVet in settings.DefaultAnalyzers,
	// `gopls cli vet` silently hides its findings.
	got := settings.VetAnalyzerNames()
	for name := range vetNames {
		if !got[name] {
			t.Errorf("analyzer %q is in cmd/vet but not marked inVet in settings.DefaultAnalyzers", name)
		}
	}
	for name := range got {
		if !vetNames[name] {
			t.Errorf("analyzer %q is marked inVet but not in cmd/vet (may indicate a stale classification)", name)
		}
	}
}

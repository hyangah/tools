// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"sort"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/protocol"
)

const (
	maxDiagsPerFile = 10
	maxDiagsTotal   = 30
)

// DiagStore collects and serves textDocument/publishDiagnostics
// notifications from all active LSP sessions. It is safe for
// concurrent use.
type DiagStore struct {
	mu    sync.Mutex
	byURI map[string]*fileDiags // keyed by file URI
}

type fileDiags struct {
	diags    []protocol.Diagnostic
	version  int32
	serverID string    // which server produced these
	updated  time.Time // when last received
}

// NewDiagStore returns an initialized DiagStore.
func NewDiagStore() *DiagStore {
	return &DiagStore{
		byURI: make(map[string]*fileDiags),
	}
}

// Update replaces the diagnostics for a given URI. Called from the
// OnDiagnostics callback in each session. If the incoming diagnostics
// are identical to the existing ones (same range+message+severity),
// the timestamp is not updated.
func (ds *DiagStore) Update(uri string, version int32, serverID string, diags []protocol.Diagnostic) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	existing := ds.byURI[uri]
	if existing != nil && diagsEqual(existing.diags, diags) {
		// Identical content: skip timestamp update to avoid stale noise.
		return
	}

	ds.byURI[uri] = &fileDiags{
		diags:    diags,
		version:  version,
		serverID: serverID,
		updated:  time.Now(),
	}
}

// ForFile returns current diagnostics for a single file URI.
// Sorted by severity (Error > Warning > Info > Hint), then by line.
// Capped at maxDiagsPerFile. Returns nil if no diagnostics.
func (ds *DiagStore) ForFile(uri string) []protocol.Diagnostic {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	fd := ds.byURI[uri]
	if fd == nil || len(fd.diags) == 0 {
		return nil
	}

	out := make([]protocol.Diagnostic, len(fd.diags))
	copy(out, fd.diags)
	sortDiags(out)
	if len(out) > maxDiagsPerFile {
		out = out[:maxDiagsPerFile]
	}
	return out
}

// ForProject returns all diagnostics across all files.
// Sorted by file URI, then severity, then line.
// Capped at maxDiagsTotal across all files. Returns nil if empty.
func (ds *DiagStore) ForProject() map[string][]protocol.Diagnostic {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if len(ds.byURI) == 0 {
		return nil
	}

	// Collect URIs with diagnostics, sorted for determinism.
	uris := make([]string, 0, len(ds.byURI))
	for uri, fd := range ds.byURI {
		if len(fd.diags) > 0 {
			uris = append(uris, uri)
		}
	}
	if len(uris) == 0 {
		return nil
	}
	sort.Strings(uris)

	result := make(map[string][]protocol.Diagnostic, len(uris))
	remaining := maxDiagsTotal
	for _, uri := range uris {
		if remaining <= 0 {
			break
		}
		fd := ds.byURI[uri]
		diags := make([]protocol.Diagnostic, len(fd.diags))
		copy(diags, fd.diags)
		sortDiags(diags)
		cap := maxDiagsPerFile
		if remaining < cap {
			cap = remaining
		}
		if len(diags) > cap {
			diags = diags[:cap]
		}
		result[uri] = diags
		remaining -= len(diags)
	}
	return result
}

// Clear removes all diagnostics for the given URI. Called when a file
// is re-synced to avoid stale diagnostics.
func (ds *DiagStore) Clear(uri string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.byURI, uri)
}

// sortDiags sorts diagnostics by severity ascending (Error=1 < Warning=2 < Info=3 < Hint=4),
// then by start line, then by start character.
func sortDiags(diags []protocol.Diagnostic) {
	sort.SliceStable(diags, func(i, j int) bool {
		si := diags[i].Severity
		sj := diags[j].Severity
		if si != sj {
			return si < sj // lower severity number = more severe = first
		}
		li := diags[i].Range.Start.Line
		lj := diags[j].Range.Start.Line
		if li != lj {
			return li < lj
		}
		return diags[i].Range.Start.Character < diags[j].Range.Start.Character
	})
}

// diagsEqual reports whether two diagnostic slices have the same
// range+message+severity for each element (order-sensitive).
func diagsEqual(a, b []protocol.Diagnostic) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Severity != b[i].Severity ||
			a[i].Message != b[i].Message ||
			a[i].Range.Start.Line != b[i].Range.Start.Line ||
			a[i].Range.Start.Character != b[i].Range.Start.Character ||
			a[i].Range.End.Line != b[i].Range.End.Line ||
			a[i].Range.End.Character != b[i].Range.End.Character {
			return false
		}
	}
	return true
}

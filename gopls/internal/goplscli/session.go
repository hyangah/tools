// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goplscli

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	filePkg "golang.org/x/tools/gopls/internal/file"
	"golang.org/x/tools/gopls/internal/golang"
	"golang.org/x/tools/gopls/internal/protocol"
)

// GoSession wraps a [cache.Session] for a single workspace root,
// providing query operations that call golang.* analysis functions
// directly. It is shared across all CLI connections querying the same
// workspace root. Safe for concurrent use.
type GoSession struct {
	root    string         // absolute path to workspace root
	session *cache.Session // concurrent-safe

	mu      sync.Mutex
	opened  map[protocol.DocumentURI]bool // overlay open tracking
	version atomic.Int32                  // monotonic version counter

	// fingerprints caches (mtime, size) to skip re-reading unchanged files.
	fingerprints map[protocol.DocumentURI]fileFingerprint
}

type fileFingerprint struct {
	mtime time.Time
	size  int64
}

// NewGoSession creates a GoSession wrapping the given [cache.Session].
// The session must already have at least one View configured.
func NewGoSession(root string, session *cache.Session) *GoSession {
	return &GoSession{
		root:         root,
		session:      session,
		opened:       make(map[protocol.DocumentURI]bool),
		fingerprints: make(map[protocol.DocumentURI]fileFingerprint),
	}
}

// Root returns the absolute path to the workspace root directory.
func (s *GoSession) Root() string { return s.root }

// Session returns the underlying [cache.Session].
func (s *GoSession) Session() *cache.Session { return s.session }

// Close shuts down the session and waits for all snapshots to be released.
func (s *GoSession) Close(ctx context.Context) {
	s.session.Shutdown(ctx)
}

// nextVersion returns a monotonically increasing version number for overlays.
func (s *GoSession) nextVersion() int32 {
	return s.version.Add(1)
}

// EnsureSynced reads a file from disk and updates the session overlay.
// It tracks open state to avoid the "modifying unopened overlay" error,
// and uses mtime+size fingerprints to skip unchanged files.
//
// The mutex is held across the entire operation to prevent TOCTOU races.
func (s *GoSession) EnsureSynced(ctx context.Context, filePath string) error {
	uri := protocol.URIFromPath(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check fingerprint — skip if unchanged.
	fp := fileFingerprint{mtime: info.ModTime(), size: info.Size()}
	if s.opened[uri] {
		if prev, ok := s.fingerprints[uri]; ok && prev == fp {
			return nil // file unchanged
		}
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	action := filePkg.Open
	if s.opened[uri] {
		action = filePkg.Change
	}

	_, err = s.session.DidModifyFiles(ctx, []filePkg.Modification{{
		URI:        uri,
		Action:     action,
		Text:       content,
		Version:    s.nextVersion(),
		LanguageID: "go",
	}})
	if err != nil {
		return fmt.Errorf("sync %s: %w", filePath, err)
	}
	s.opened[uri] = true
	s.fingerprints[uri] = fp
	return nil
}

// ForceSync re-reads the file regardless of fingerprint.
func (s *GoSession) ForceSync(ctx context.Context, filePath string) error {
	uri := protocol.URIFromPath(filePath)

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	action := filePkg.Open
	if s.opened[uri] {
		action = filePkg.Change
	}

	_, err = s.session.DidModifyFiles(ctx, []filePkg.Modification{{
		URI:        uri,
		Action:     action,
		Text:       content,
		Version:    s.nextVersion(),
		LanguageID: "go",
	}})
	if err != nil {
		return fmt.Errorf("sync %s: %w", filePath, err)
	}
	s.opened[uri] = true

	// Update fingerprint.
	if info, err := os.Stat(filePath); err == nil {
		s.fingerprints[uri] = fileFingerprint{mtime: info.ModTime(), size: info.Size()}
	} else {
		delete(s.fingerprints, uri)
	}
	return nil
}

// Definition returns the definition location(s) for the given position.
func (s *GoSession) Definition(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) ([]protocol.Location, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.Definition(ctx, snapshot, fh, rng)
}

// References returns all references to the identifier at the given position.
func (s *GoSession) References(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range, includeDeclaration bool) ([]protocol.Location, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.References(ctx, snapshot, fh, rng, includeDeclaration)
}

// Hover returns hover information for the given position.
func (s *GoSession) Hover(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) (*protocol.Hover, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.Hover(ctx, snapshot, fh, rng, nil)
}

// Implementation returns implementations of the type/interface at the given position.
func (s *GoSession) Implementation(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) ([]protocol.Location, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.Implementation(ctx, snapshot, fh, rng)
}

// DocumentSymbols returns all symbols in the given file.
func (s *GoSession) DocumentSymbols(ctx context.Context, uri protocol.DocumentURI) ([]protocol.DocumentSymbol, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.DocumentSymbols(ctx, snapshot, fh)
}

// WorkspaceSymbols searches for symbols matching the query across all views.
func (s *GoSession) WorkspaceSymbols(ctx context.Context, query string) ([]protocol.SymbolInformation, error) {
	views := s.session.Views()
	var snapshots []*cache.Snapshot
	var releases []func()
	for _, v := range views {
		snap, release, err := v.Snapshot()
		if err != nil {
			// Release any already acquired.
			for _, r := range releases {
				r()
			}
			return nil, fmt.Errorf("snapshot for view: %w", err)
		}
		snapshots = append(snapshots, snap)
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	return golang.WorkspaceSymbols(ctx, snapshots, query, golang.WorkspaceSymbolsOptions{})
}

// Rename renames the identifier at the given position.
func (s *GoSession) Rename(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range, newName string) ([]protocol.DocumentChange, error) {
	fh, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.Rename(ctx, snapshot, fh, rng, newName)
}

// DiagnoseFile returns diagnostics for the given file.
func (s *GoSession) DiagnoseFile(ctx context.Context, uri protocol.DocumentURI) ([]*cache.Diagnostic, error) {
	_, snapshot, release, err := s.session.FileOf(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer release()
	return golang.DiagnoseFile(ctx, snapshot, uri)
}

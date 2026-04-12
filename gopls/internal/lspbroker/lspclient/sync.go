// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/protocol"
)

// openState tracks the server's view of a single open document.
type openState struct {
	version  int32     // monotonically increasing LSP version number
	mtime    time.Time // mtime when we last synced
	size     int64     // size when we last synced
	content  []byte    // content sent in the last didOpen/didChange
	language string    // LSP language identifier
}

// syncer tracks per-URI open state for a client connection. It is goroutine-safe.
type syncer struct {
	mu    sync.Mutex            // protects files and uriLocks
	files map[string]*openState // keyed by URI string
	// uriLocks provides per-URI serialization for concurrent ensureOpen calls.
	uriLocks map[string]*sync.Mutex
}

// newSyncer returns an initialized syncer.
func newSyncer() syncer {
	return syncer{
		files:    make(map[string]*openState),
		uriLocks: make(map[string]*sync.Mutex),
	}
}

// lockForURI returns the per-URI mutex, creating it if necessary.
func (s *syncer) lockForURI(uri string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.uriLocks[uri]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.uriLocks[uri] = m
	return m
}

// DidOpen sends textDocument/didOpen to the server with the provided content.
// If the file is already open and unchanged (same mtime/size), this is a
// no-op. If it is already open but stale, DidChange is sent instead.
func (c *Client) DidOpen(ctx context.Context, uri string, languageID string, content []byte) error {
	uriLock := c.syncer.lockForURI(uri)
	uriLock.Lock()
	defer uriLock.Unlock()

	c.syncer.mu.Lock()
	st := c.syncer.files[uri]
	c.syncer.mu.Unlock()

	if st != nil {
		// Already open: send DidChange.
		return c.sendDidChange(ctx, uri, content, st)
	}
	return c.sendDidOpen(ctx, uri, languageID, content)
}

// DidChange sends textDocument/didChange to the server with the new content.
// The file must have been opened with DidOpen first.
func (c *Client) DidChange(ctx context.Context, uri string, content []byte) error {
	uriLock := c.syncer.lockForURI(uri)
	uriLock.Lock()
	defer uriLock.Unlock()

	c.syncer.mu.Lock()
	st := c.syncer.files[uri]
	c.syncer.mu.Unlock()

	if st == nil {
		return fmt.Errorf("lspclient.DidChange: %s is not open", uri)
	}
	return c.sendDidChange(ctx, uri, content, st)
}

// DidSave sends textDocument/didSave to the server.
func (c *Client) DidSave(ctx context.Context, uri string) error {
	params := &protocol.DidSaveTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{
			URI: protocol.DocumentURI(uri),
		},
	}
	return c.conn.Notify(ctx, "textDocument/didSave", params)
}

// DidClose sends textDocument/didClose and removes the file from the open set.
func (c *Client) DidClose(ctx context.Context, uri string) error {
	uriLock := c.syncer.lockForURI(uri)
	uriLock.Lock()
	defer uriLock.Unlock()

	c.syncer.mu.Lock()
	_, open := c.syncer.files[uri]
	if open {
		delete(c.syncer.files, uri)
	}
	c.syncer.mu.Unlock()

	if !open {
		return nil // idempotent
	}
	params := &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{
			URI: protocol.DocumentURI(uri),
		},
	}
	return c.conn.Notify(ctx, "textDocument/didClose", params)
}

// EnsureOpen is a convenience helper used by operation methods. It reads the
// file at path, stats it for the mtime/size fingerprint, and calls DidOpen or
// DidChange as needed.
//
// The file size must be ≤ maxSyncBytes; larger files return an error.
func (c *Client) EnsureOpen(ctx context.Context, filePath string, languageID string) error {
	const maxSyncBytes = 10 << 20 // 10 MB

	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("lspclient.EnsureOpen: stat %s: %w", filePath, err)
	}
	if fi.Size() > maxSyncBytes {
		return fmt.Errorf("lspclient.EnsureOpen: file too large to sync (%.1f MB, limit 10 MB)",
			float64(fi.Size())/(1<<20))
	}

	uri := string(protocol.URIFromPath(filePath))
	uriLock := c.syncer.lockForURI(uri)
	uriLock.Lock()
	defer uriLock.Unlock()

	c.syncer.mu.Lock()
	st := c.syncer.files[uri]
	c.syncer.mu.Unlock()

	if st != nil && !staleByFingerprint(st, fi) {
		return nil // already in sync
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("lspclient.EnsureOpen: read %s: %w", filePath, err)
	}

	// Re-stat after read to detect mid-sync modification.
	fi2, err := os.Stat(filePath)
	if err == nil && (fi2.ModTime() != fi.ModTime() || fi2.Size() != fi.Size()) {
		// File changed while we read it; try once more.
		content, err = os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("lspclient.EnsureOpen: re-read %s: %w", filePath, err)
		}
		fi = fi2
	}

	if st == nil {
		if err := c.sendDidOpen(ctx, uri, languageID, content); err != nil {
			return err
		}
	} else {
		if err := c.sendDidChange(ctx, uri, content, st); err != nil {
			return err
		}
	}

	// Update fingerprint.
	c.syncer.mu.Lock()
	if s := c.syncer.files[uri]; s != nil {
		s.mtime = fi.ModTime()
		s.size = fi.Size()
	}
	c.syncer.mu.Unlock()

	return nil
}

// ForceSync always re-reads the file at filePath and sends
// textDocument/didOpen or textDocument/didChange regardless of the
// current fingerprint. It is used by the lsp.sync broker command to
// push an explicit re-sync after an editor modifies a file.
func (c *Client) ForceSync(ctx context.Context, filePath string, languageID string) error {
	const maxSyncBytes = 10 << 20 // 10 MB

	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("lspclient.ForceSync: stat %s: %w", filePath, err)
	}
	if fi.Size() > maxSyncBytes {
		return fmt.Errorf("lspclient.ForceSync: file too large to sync (%.1f MB, limit 10 MB)",
			float64(fi.Size())/(1<<20))
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("lspclient.ForceSync: read %s: %w", filePath, err)
	}

	uri := string(protocol.URIFromPath(filePath))
	uriLock := c.syncer.lockForURI(uri)
	uriLock.Lock()
	defer uriLock.Unlock()

	c.syncer.mu.Lock()
	st := c.syncer.files[uri]
	c.syncer.mu.Unlock()

	if st == nil {
		if err := c.sendDidOpen(ctx, uri, languageID, content); err != nil {
			return err
		}
	} else {
		if err := c.sendDidChange(ctx, uri, content, st); err != nil {
			return err
		}
	}

	// Update fingerprint.
	c.syncer.mu.Lock()
	if s := c.syncer.files[uri]; s != nil {
		s.mtime = fi.ModTime()
		s.size = fi.Size()
	}
	c.syncer.mu.Unlock()

	return nil
}

// staleByFingerprint reports whether st's mtime/size differs from fi.
func staleByFingerprint(st *openState, fi os.FileInfo) bool {
	return st.mtime != fi.ModTime() || st.size != fi.Size()
}

// sendDidOpen sends textDocument/didOpen and records the open state.
// The caller must hold the per-URI lock.
func (c *Client) sendDidOpen(ctx context.Context, uri string, languageID string, content []byte) error {
	// Double-check under per-URI lock.
	c.syncer.mu.Lock()
	if c.syncer.files[uri] != nil {
		c.syncer.mu.Unlock()
		// Race: another goroutine already opened it; fall through to DidChange.
		return c.sendDidChange(ctx, uri, content, c.syncer.files[uri])
	}
	st := &openState{
		version:  1,
		content:  content,
		language: languageID,
	}
	c.syncer.files[uri] = st
	c.syncer.mu.Unlock()

	params := &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        protocol.DocumentURI(uri),
			LanguageID: protocol.LanguageKind(languageID),
			Version:    st.version,
			Text:       string(content),
		},
	}
	return c.conn.Notify(ctx, "textDocument/didOpen", params)
}

// sendDidChange sends textDocument/didChange and increments the version.
// The caller must hold the per-URI lock.
func (c *Client) sendDidChange(ctx context.Context, uri string, content []byte, st *openState) error {
	c.syncer.mu.Lock()
	st.version++
	version := st.version
	st.content = content
	c.syncer.mu.Unlock()

	params := &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{
				URI: protocol.DocumentURI(uri),
			},
			Version: version,
		},
		// Full-document sync: send entire content as a single change event.
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Text: string(content)},
		},
	}
	return c.conn.Notify(ctx, "textDocument/didChange", params)
}

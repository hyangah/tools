// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package goadapter implements the Go-specific language server adapter
// for the LSP broker.
//
// It spawns and manages a gopls subprocess (or connects to an existing
// daemon in Phase 2) and translates broker protocol requests into LSP
// calls over the gopls stdio interface.
//
// The primary entry point is [NewGoSession], which returns a
// [lspbroker.Session] implementation for a given Go workspace root.
// The session lazily starts a gopls subprocess on the first request
// and keeps it running until [GoSession.Close] is called.
//
// Phase 1 uses subprocess mode only (plain "gopls serve"). Phase 2
// will switch to "-remote=auto" forwarder mode so that the broker
// shares an existing editor gopls daemon.
//
// TODO(WS-D Phase 2): add gopls -remote=auto forwarder mode.
package goadapter

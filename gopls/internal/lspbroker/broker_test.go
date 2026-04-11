// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"

	"golang.org/x/tools/internal/jsonrpc2"
)

// startTestBroker starts a Broker on an in-process unix socket under
// t.TempDir() and returns the connected jsonrpc2.Conn for the client
// side. The broker and listener are cleaned up via t.Cleanup.
func startTestBroker(t *testing.T) (conn jsonrpc2.Conn, cacheDir string) {
	t.Helper()
	cacheDir = t.TempDir()

	l, err := NewListener(cacheDir)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	b := NewBroker("/test/gopls", "test")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		b.Stop(context.Background())
	})
	go func() {
		if err := b.Serve(ctx, l); err != nil && ctx.Err() == nil {
			t.Logf("broker.Serve: %v", err)
		}
	}()

	// Use the listener's actual address — NewListener may have fallen
	// back to a shorter path when the cacheDir path was too long for
	// macOS's 104-byte unix socket limit.
	nc, err := net.Dial(l.Addr().Network(), l.Addr().String())
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	stream := jsonrpc2.NewHeaderStream(nc)
	conn = jsonrpc2.NewConn(stream)
	conn.Go(ctx, jsonrpc2.MethodNotFound)
	t.Cleanup(func() { conn.Close() })

	return conn, cacheDir
}

func TestBroker_HandshakeSuccess(t *testing.T) {
	conn, _ := startTestBroker(t)
	ctx := context.Background()

	goplsPath, _ := os.Executable()
	resp, err := Handshake(ctx, conn, goplsPath, "test")
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if resp.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", resp.ProtocolVersion, ProtocolVersion)
	}
	if resp.DaemonPID <= 0 {
		t.Errorf("DaemonPID = %d, want > 0", resp.DaemonPID)
	}
}

func TestBroker_RejectMethodBeforeHandshake(t *testing.T) {
	conn, _ := startTestBroker(t)
	ctx := context.Background()

	// Send lsp.definition without handshake; expect an error.
	params := DefinitionParams{
		Version:   ProtocolVersion,
		File:      "/tmp/foo.go",
		Line:      1,
		Character: 1,
	}
	var result json.RawMessage
	_, err := conn.Call(ctx, DefinitionMethod, params, &result)
	if err == nil {
		t.Fatal("expected error before handshake, got nil")
	}
}

func TestBroker_DefinitionStub(t *testing.T) {
	conn, _ := startTestBroker(t)
	ctx := context.Background()

	goplsPath, _ := os.Executable()
	if _, err := Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	// After handshake, lsp.definition should return a session error
	// (Phase 1 stub — no LSP server configured).
	params := DefinitionParams{
		Version:   ProtocolVersion,
		File:      "/tmp/nonexistent/foo.go",
		Line:      1,
		Character: 1,
	}
	var result json.RawMessage
	_, err := conn.Call(ctx, DefinitionMethod, params, &result)
	// Phase 1: expect an error because the stub session returns
	// "method not implemented". A nil error here means the stub
	// accidentally returned a result.
	if err == nil {
		t.Fatal("expected stub error from lsp.definition, got nil")
	}
}

func TestBroker_HandshakeVersionMismatch(t *testing.T) {
	conn, _ := startTestBroker(t)
	ctx := context.Background()

	// Send a handshake with a wrong protocol version.
	req := HandshakeRequest{
		ProtocolVersion: ProtocolVersion + 99,
		GoplsPath:       "/test/gopls",
		ClientPID:       os.Getpid(),
	}
	var resp HandshakeResponse
	if _, err := conn.Call(ctx, HandshakeMethod, req, &resp); err != nil {
		t.Fatalf("handshake call: %v", err)
	}
	// The daemon replies with its real version.
	if resp.ProtocolVersion != ProtocolVersion {
		t.Errorf("daemon returned version %d, want %d", resp.ProtocolVersion, ProtocolVersion)
	}
}

func TestBroker_Sessions(t *testing.T) {
	conn, _ := startTestBroker(t)
	ctx := context.Background()

	goplsPath, _ := os.Executable()
	if _, err := Handshake(ctx, conn, goplsPath, "test"); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	// Trigger a session via a definition request (even though it returns
	// an error, the session object is created).
	params := DefinitionParams{
		Version:   ProtocolVersion,
		File:      "/tmp/some/file.go",
		Line:      1,
		Character: 1,
	}
	var result json.RawMessage
	conn.Call(ctx, DefinitionMethod, params, &result) //nolint:errcheck // error expected
}

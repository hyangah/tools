// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/tools/internal/jsonrpc2"
)

// ProtocolVersion is the version of the broker wire protocol spoken by
// this build. It is exchanged in [HandshakeRequest] and
// [HandshakeResponse] and also carried on every lsp.* request via the
// Version field.
//
// A mismatch between the CLI and the daemon on this value is a hard
// error and causes the CLI to exit with code 4 after suggesting
// "gopls lspcli daemon restart". In normal operation the build-id-hashed
// cache directory (see [CacheRoot]) prevents two incompatible builds
// from sharing a socket in the first place; the handshake and the
// per-request version field are belt-and-suspenders defenses. See
// the project's ADR-005 for the full rationale.
const ProtocolVersion = 1

// HandshakeMethod is the JSON-RPC method name the CLI uses to open
// every fresh connection to the daemon. The daemon must reject any
// other method on an un-handshaked connection with a JSON-RPC
// -32600 invalid_request error.
const HandshakeMethod = "broker.handshake"

// HandshakeRequest is the first RPC the CLI sends on every newly
// opened connection to the broker daemon.
//
// The request carries enough identity for the daemon to decide whether
// to accept the connection. See [HandshakeResponse] for the symmetric
// information the daemon returns.
type HandshakeRequest struct {
	// ProtocolVersion is the broker wire protocol version spoken by
	// the CLI. It is compared against the daemon's [ProtocolVersion]
	// and a mismatch is fatal.
	ProtocolVersion int `json:"protocolVersion"`

	// GoplsPath is the absolute path to the gopls binary that the CLI
	// is running under (os.Executable()). The daemon logs a warning
	// on mismatch but does not fail the handshake — the build-id
	// hashed socket path is expected to have routed incompatible
	// builds to different sockets already.
	GoplsPath string `json:"goplsPath"`

	// GoplsVersion is the gopls version string of the CLI, as
	// reported by golang.org/x/tools/gopls/internal/version. An
	// empty string is legal and means "unknown".
	GoplsVersion string `json:"goplsVersion,omitempty"`

	// ClientPID is the operating-system process ID of the CLI
	// invocation. The daemon may log it for debugging but must not
	// rely on it for authorization — the unix-socket file mode
	// already restricts access to the owning user.
	ClientPID int `json:"clientPID"`
}

// HandshakeResponse is the daemon's reply to a [HandshakeRequest]. It
// mirrors the request so that a CLI can detect drift in either
// direction, and adds daemon-only state (PID, uptime) useful for
// diagnostics.
type HandshakeResponse struct {
	// ProtocolVersion is the broker wire protocol version spoken by
	// the daemon. On mismatch with the CLI's [HandshakeRequest], the
	// daemon still responds with its true value so the CLI can
	// produce a helpful error message.
	ProtocolVersion int `json:"protocolVersion"`

	// GoplsPath is the absolute path to the gopls binary that the
	// daemon is running under.
	GoplsPath string `json:"goplsPath"`

	// GoplsVersion is the gopls version string of the daemon.
	GoplsVersion string `json:"goplsVersion,omitempty"`

	// DaemonPID is the operating-system process ID of the daemon.
	DaemonPID int `json:"daemonPID"`

	// UptimeSec is the number of seconds since the daemon's socket
	// listener began accepting connections.
	UptimeSec int64 `json:"uptimeSec"`
}

// Handshake performs the broker.handshake RPC on conn and validates
// the response. It is the first call a CLI must make on every fresh
// connection before issuing any lsp.* method.
//
// If the daemon's ProtocolVersion differs from [ProtocolVersion],
// Handshake returns an error that wraps [ErrVersionMismatch] and
// suggests running "gopls lspcli daemon restart".
//
// goplsPath and goplsVersion are the CLI's own values; they are sent
// to the daemon for logging and diagnostic purposes. Pass empty strings
// if not available.
func Handshake(ctx context.Context, conn jsonrpc2.Conn, goplsPath, goplsVersion string) (*HandshakeResponse, error) {
	req := HandshakeRequest{
		ProtocolVersion: ProtocolVersion,
		GoplsPath:       goplsPath,
		GoplsVersion:    goplsVersion,
		ClientPID:       os.Getpid(),
	}
	var resp HandshakeResponse
	if _, err := conn.Call(ctx, HandshakeMethod, req, &resp); err != nil {
		return nil, fmt.Errorf("broker.handshake: %w", err)
	}
	if resp.ProtocolVersion != ProtocolVersion {
		return &resp, fmt.Errorf("%w: cli=%d daemon=%d; run: gopls lspcli daemon restart",
			ErrVersionMismatch, ProtocolVersion, resp.ProtocolVersion)
	}
	return &resp, nil
}

// StopMethod is the JSON-RPC method name for the broker.stop request.
// A client sends this to request a graceful daemon shutdown.
const StopMethod = "broker.stop"

// DefinitionMethod is the JSON-RPC method name for the lsp.definition request.
const DefinitionMethod = "lsp.definition"

// DefinitionParams are the parameters for an [lsp.definition] request.
// File must be an absolute path; the CLI resolves relative paths before
// sending. Line and Character are 1-based (matching what users type on
// the command line); the broker converts to 0-based before forwarding
// to the LSP server.
type DefinitionParams struct {
	// Version is the broker protocol version for belt-and-suspenders
	// checking. Must equal [ProtocolVersion].
	Version int `json:"version"`

	// File is the absolute path to the source file.
	File string `json:"file"`

	// Line is the 1-based line number of the cursor position.
	Line int `json:"line"`

	// Character is the 1-based character offset of the cursor position.
	Character int `json:"character"`
}

// Position is a 0-based line/character offset, matching LSP's Position
// type. Returned in broker results after the broker converts from the
// 1-based wire format.
type Position struct {
	// Line is the 0-based line number.
	Line int `json:"line"`

	// Character is the 0-based UTF-16 character offset.
	Character int `json:"character"`
}

// Range is a start/end [Position] pair, matching LSP's Range type.
type Range struct {
	// Start is the inclusive start of the range.
	Start Position `json:"start"`

	// End is the exclusive end of the range.
	End Position `json:"end"`
}

// Location is a file URI and [Range], matching LSP's Location type.
// Returned as elements of [DefinitionResult].
type Location struct {
	// URI is the file URI, e.g. "file:///abs/path/to/foo.go".
	URI string `json:"uri"`

	// Range is the source range within the file.
	Range Range `json:"range"`
}

// Broker-specific JSON-RPC error codes. These extend the standard
// JSON-RPC 2.0 error code range (-32768 to -32000).
const (
	// ErrCodeNoServer is returned when no language server is configured
	// for the requested file extension.
	ErrCodeNoServer = -32001

	// ErrCodeServerCrashed is returned when the LSP server process died
	// and could not be restarted within the retry limit.
	ErrCodeServerCrashed = -32002

	// ErrCodeTimeout is returned when the LSP server did not respond
	// within the per-operation timeout.
	ErrCodeTimeout = -32003

	// ErrCodeFileNotFound is returned when the requested file does not
	// exist on disk.
	ErrCodeFileNotFound = -32004

	// ErrCodeProjectNotFound is returned when the broker cannot find a
	// project root (e.g. no go.mod or .lsp.json ancestor directory).
	ErrCodeProjectNotFound = -32005

	// ErrCodeVersionMismatch is returned when the CLI and daemon speak
	// different broker protocol versions.
	ErrCodeVersionMismatch = -32006

	// ErrCodeUntrustedRoot is returned when the project root is not in
	// the trust list.
	ErrCodeUntrustedRoot = -32007

	// ErrCodeContentModified is the LSP-spec code -32801. It surfaces
	// only after all transparent retries in the broker are exhausted.
	ErrCodeContentModified = -32801
)

// Broker-specific sentinel errors. Use errors.Is or errors.As to
// unwrap from jsonrpc2.WireError values.
var (
	// ErrNoServer is returned when no language server is configured for
	// the requested file extension.
	ErrNoServer = jsonrpc2.NewError(ErrCodeNoServer, "no language server configured for this file type")

	// ErrServerCrashed is returned when the LSP server process died and
	// exhausted all restart attempts.
	ErrServerCrashed = jsonrpc2.NewError(ErrCodeServerCrashed, "language server crashed")

	// ErrTimeout is returned when the LSP server did not respond in
	// time.
	ErrTimeout = jsonrpc2.NewError(ErrCodeTimeout, "language server did not respond in time")

	// ErrFileNotFound is returned when the requested file path does not
	// exist on disk.
	ErrFileNotFound = jsonrpc2.NewError(ErrCodeFileNotFound, "file not found")

	// ErrProjectNotFound is returned when no project root can be
	// determined for the requested file.
	ErrProjectNotFound = jsonrpc2.NewError(ErrCodeProjectNotFound, "project root not found")

	// ErrVersionMismatch is returned when the CLI and daemon speak
	// incompatible broker protocol versions.
	ErrVersionMismatch = jsonrpc2.NewError(ErrCodeVersionMismatch, "broker protocol version mismatch")

	// ErrUntrustedRoot is returned when the project root has not been
	// added to the user's trust list.
	ErrUntrustedRoot = jsonrpc2.NewError(ErrCodeUntrustedRoot, "project root not trusted")
)

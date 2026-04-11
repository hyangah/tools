// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

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

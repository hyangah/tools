// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/tools/internal/event"
	"golang.org/x/tools/internal/jsonrpc2"
)

// Broker is the top-level daemon object. It listens on a unix domain
// socket under [CacheRoot], accepts incoming broker-protocol connections
// from lspcli, and dispatches requests to per-workspace [Session]
// objects.
//
// Create a Broker with [NewBroker], then call [Broker.Serve] with a
// net.Listener obtained from [NewListener]. [Broker.Stop] initiates
// graceful shutdown.
type Broker struct {
	goplsPath    string
	goplsVersion string
	startTime    time.Time

	// mu protects sessions and stopped.
	mu       sync.Mutex
	sessions map[string]Session // keyed by workspace root
	stopped  bool

	// cancel shuts down the Serve loop when called.
	cancel context.CancelFunc
}

// NewBroker returns a new [Broker] with the given identity strings.
// goplsPath should be os.Executable(); goplsVersion is the gopls
// version string (may be empty).
func NewBroker(goplsPath, goplsVersion string) *Broker {
	return &Broker{
		goplsPath:    goplsPath,
		goplsVersion: goplsVersion,
		sessions:     make(map[string]Session),
	}
}

// Sessions returns a snapshot of the currently active sessions.
func (b *Broker) Sessions() []SessionInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]SessionInfo, 0, len(b.sessions))
	for root := range b.sessions {
		out = append(out, SessionInfo{Root: root})
	}
	return out
}

// Stop initiates graceful shutdown of the broker. It cancels the
// context passed to [Broker.Serve] and closes all active sessions.
// Stop may be called from any goroutine. It is idempotent.
func (b *Broker) Stop(ctx context.Context) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return nil
	}
	b.stopped = true
	cancel := b.cancel
	sessions := b.sessions
	b.sessions = nil
	b.mu.Unlock()

	for _, s := range sessions {
		if err := s.Close(); err != nil {
			event.Error(ctx, "closing session", err)
		}
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

// Serve starts accepting jsonrpc2 connections on l. It blocks until l
// is closed or Stop is called. The listener is not closed by Serve; the
// caller is responsible for closing it.
//
// Serve stores a cancel function so that Stop can unblock the serve
// loop. Only one goroutine may call Serve at a time.
func (b *Broker) Serve(ctx context.Context, l net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.startTime = time.Now()
	b.mu.Unlock()
	defer cancel()

	server := jsonrpc2.HandlerServer(b.newConnHandler())
	return jsonrpc2.Serve(ctx, l, server, 0)
}

// newConnHandler returns a jsonrpc2.Handler that manages one
// connection. Each call creates a fresh closure that tracks whether
// the broker.handshake RPC has been performed on this connection.
// All lsp.* methods are rejected until the handshake succeeds.
func (b *Broker) newConnHandler() jsonrpc2.Handler {
	var (
		mu         sync.Mutex
		handshaked bool
	)
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		mu.Lock()
		hs := handshaked
		mu.Unlock()

		if !hs {
			if req.Method() != HandshakeMethod {
				return reply(ctx, nil, fmt.Errorf("%w: must call %s first",
					jsonrpc2.ErrInvalidRequest, HandshakeMethod))
			}
			var params HandshakeRequest
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, fmt.Errorf("%w: %v", jsonrpc2.ErrInvalidParams, err))
			}

			b.mu.Lock()
			uptime := int64(time.Since(b.startTime).Seconds())
			b.mu.Unlock()

			resp := HandshakeResponse{
				ProtocolVersion: ProtocolVersion,
				GoplsPath:       b.goplsPath,
				GoplsVersion:    b.goplsVersion,
				DaemonPID:       os.Getpid(),
				UptimeSec:       uptime,
			}
			// Reply with our version regardless of mismatch so the CLI
			// can produce a helpful error message.
			if err := reply(ctx, resp, nil); err != nil {
				return err
			}
			if params.ProtocolVersion != ProtocolVersion {
				// Mismatch: we replied but the connection is not usable.
				event.Log(ctx, fmt.Sprintf("broker.handshake: version mismatch cli=%d daemon=%d",
					params.ProtocolVersion, ProtocolVersion))
				return nil
			}
			mu.Lock()
			handshaked = true
			mu.Unlock()
			return nil
		}

		return b.dispatch(ctx, reply, req)
	}
}

// dispatch routes a post-handshake request to the appropriate handler.
// In Phase 1 all lsp.* methods return a "not yet implemented" response.
func (b *Broker) dispatch(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case DefinitionMethod:
		return b.handleDefinition(ctx, reply, req)
	default:
		return reply(ctx, nil, fmt.Errorf("%w: %q", jsonrpc2.ErrMethodNotFound, req.Method()))
	}
}

// handleDefinition handles an lsp.definition request. In Phase 1 the
// session dispatch is a stub and returns ErrNoServer. WS-E and WS-C
// replace this with a real implementation in Phase 2/3.
func (b *Broker) handleDefinition(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	var params DefinitionParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return reply(ctx, nil, fmt.Errorf("%w: %v", jsonrpc2.ErrInvalidParams, err))
	}
	if params.File == "" {
		return reply(ctx, nil, fmt.Errorf("%w: file is required", jsonrpc2.ErrInvalidParams))
	}
	if !filepath.IsAbs(params.File) {
		return reply(ctx, nil, fmt.Errorf("%w: file must be an absolute path", jsonrpc2.ErrInvalidParams))
	}

	sess, err := b.sessionForFile(ctx, params.File)
	if err != nil {
		return reply(ctx, nil, err)
	}
	raw, err := sess.Handle(ctx, DefinitionMethod, req.Params())
	if err != nil {
		return reply(ctx, nil, err)
	}
	return reply(ctx, json.RawMessage(raw), nil)
}

// sessionForFile returns the [Session] responsible for the given
// absolute file path, creating one if none exists. The session root is
// determined by walking up the directory tree to find the nearest
// directory. In Phase 1 no project-root detection is performed; the
// file's parent directory is used as the root.
func (b *Broker) sessionForFile(ctx context.Context, file string) (Session, error) {
	root := filepath.Dir(file)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return nil, fmt.Errorf("broker is stopped")
	}
	if s, ok := b.sessions[root]; ok {
		return s, nil
	}
	s := newSession(root)
	b.sessions[root] = s
	event.Log(ctx, fmt.Sprintf("broker: new session for root %s", root))
	return s, nil
}

// maxUnixSockPath is the maximum length of a unix socket path.
// macOS enforces a hard limit of 104 bytes; Linux allows up to 108.
// We use 104 to be safe on all POSIX platforms.
const maxUnixSockPath = 104

// NewListener creates a unix domain socket listener at
// cacheDir/broker.sock. The cacheDir is created if it does not exist.
// The socket is created with mode 0600 so only the owning user can
// connect.
//
// If the constructed path would exceed [maxUnixSockPath] bytes (a
// macOS constraint), NewListener falls back to a shorter path under
// os.TempDir() that embeds the [BuildIDHash] for version isolation.
func NewListener(cacheDir string) (net.Listener, error) {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir %s: %w", cacheDir, err)
	}
	sockPath := filepath.Join(cacheDir, "broker.sock")
	if len(sockPath) > maxUnixSockPath {
		// Fall back to a short path under $TMPDIR.
		shortDir := filepath.Join(os.TempDir(), "lspbk-"+BuildIDHash())
		if err := os.MkdirAll(shortDir, 0o700); err != nil {
			return nil, fmt.Errorf("create fallback socket dir %s: %w", shortDir, err)
		}
		sockPath = filepath.Join(shortDir, "broker.sock")
	}
	// Remove a stale socket if present (e.g. from a previous crash).
	// A live daemon would have been caught by the pidfile check in the
	// caller, so this is safe.
	_ = os.Remove(sockPath)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	// Restrict socket access to the owning user.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("chmod socket %s: %w", sockPath, err)
	}
	return l, nil
}

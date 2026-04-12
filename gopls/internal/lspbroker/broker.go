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
	"strings"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/event"
	"golang.org/x/tools/internal/jsonrpc2"
)

// sessionKey identifies a unique session: one per (project root, server ID).
type sessionKey struct {
	root     string // absolute path to project root
	serverID string // e.g. "go", "typescript", or "override" (tests)
}

// configEntry caches a loaded .lsp.json config together with the file's
// modification time so that sessionForFile can detect when the config
// changes on disk and invalidate stale sessions.
type configEntry struct {
	cfg   *Config
	mtime time.Time // zero when .lsp.json is absent
}

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

	// GoSessionFactory creates a [Session] for Go source files. It is
	// called when sessionForFile determines a file belongs to a Go
	// project (go.mod/go.work detected). Set by daemon startup code
	// to wrap goadapter.NewGoSession. If nil, Go files return
	// [ErrNoServer].
	GoSessionFactory func(root string) Session

	// SessionOverride, if non-nil, bypasses all config-aware routing
	// in sessionForFile. Every request creates a session via this
	// function keyed by filepath.Dir(file). Used in tests.
	SessionOverride func(root string) Session

	// TrustStore, if non-nil, is consulted before loading .lsp.json
	// configs. Untrusted roots with .lsp.json present return
	// [ErrUntrustedRoot]. Go auto-config (no .lsp.json) skips the
	// trust check. If nil, all roots are trusted.
	TrustStore *TrustStore

	// IdleTimeout is how long the broker waits with no requests before
	// shutting itself down. Zero means no idle timeout.
	IdleTimeout time.Duration

	// Diags collects publishDiagnostics notifications from all active
	// LSP sessions. Created by NewBroker.
	Diags *DiagStore

	// mu protects sessions, configCache, stopped, idle, and listener.
	mu          sync.Mutex
	sessions    map[sessionKey]Session
	configCache map[string]configEntry // keyed by project root
	stopped     bool
	idle        *idleTracker // nil if IdleTimeout == 0
	listener    net.Listener // set by Serve, closed by Stop

	// cancel shuts down the Serve loop when called.
	cancel context.CancelFunc
}

// NewBroker returns a new [Broker] with the given identity strings.
//
// goplsPath should be os.Executable(); goplsVersion is the gopls
// version string (may be empty).
//
// After construction, set [Broker.GoSessionFactory] to enable Go
// support and optionally set [Broker.SessionOverride] for tests.
func NewBroker(goplsPath, goplsVersion string) *Broker {
	return &Broker{
		goplsPath:    goplsPath,
		goplsVersion: goplsVersion,
		sessions:     make(map[sessionKey]Session),
		configCache:  make(map[string]configEntry),
		Diags:        NewDiagStore(),
	}
}

// Sessions returns a snapshot of the currently active sessions.
func (b *Broker) Sessions() []SessionInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]SessionInfo, 0, len(b.sessions))
	for key := range b.sessions {
		out = append(out, SessionInfo{Root: key.root})
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
	idle := b.idle
	b.idle = nil
	l := b.listener
	b.listener = nil
	b.mu.Unlock()

	if idle != nil {
		idle.stop()
	}

	for _, s := range sessions {
		if err := s.Close(); err != nil {
			event.Error(ctx, "closing session", err)
		}
	}
	// Close the listener to unblock Accept in jsonrpc2.Serve.
	if l != nil {
		l.Close()
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
	b.listener = l
	if b.IdleTimeout > 0 {
		b.idle = newIdleTracker(b.IdleTimeout, func() {
			b.Stop(context.Background())
		})
	}
	b.mu.Unlock()
	defer cancel()

	// Use a per-connection server so each connection gets its own fresh
	// handler with its own handshaked state. A shared handler would
	// cause the second connection to skip the handshake check because
	// the first connection set handshaked=true.
	server := jsonrpc2.ServerFunc(func(ctx context.Context, conn jsonrpc2.Conn) error {
		conn.Go(ctx, b.newConnHandler())
		<-conn.Done()
		return conn.Err()
	})
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
		// Bump idle timer on any incoming request (including handshake).
		b.mu.Lock()
		idle := b.idle
		b.mu.Unlock()
		if idle != nil {
			idle.bump()
		}

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
func (b *Broker) dispatch(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case StopMethod:
		// Reply before stopping so the client receives the response.
		if err := reply(ctx, nil, nil); err != nil {
			return err
		}
		go b.Stop(context.Background())
		return nil
	case DefinitionMethod, ReferencesMethod, HoverMethod, ImplementationMethod,
		PrepareCallHierarchyMethod:
		return b.handlePositionalOp(ctx, reply, req)
	case DocumentSymbolMethod, WorkspaceSymbolMethod,
		IncomingCallsMethod, OutgoingCallsMethod:
		return b.handlePassthrough(ctx, reply, req)
	case DiagnosticsMethod:
		return b.handleDiagnostics(ctx, reply, req)
	case SyncMethod:
		return b.handleSync(ctx, reply, req)
	default:
		return reply(ctx, nil, fmt.Errorf("%w: %q", jsonrpc2.ErrMethodNotFound, req.Method()))
	}
}

// handlePositionalOp handles any position-taking operation (def, refs, hover,
// impl, prepareCallHierarchy). All use the same discriminated parameter shape
// per ADR-007/008: Form A (name-based) or Form B (positional bypass).
func (b *Broker) handlePositionalOp(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
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
	if params.Symbol != "" && params.Character != nil {
		return reply(ctx, nil, fmt.Errorf("%w: symbol and character are mutually exclusive", jsonrpc2.ErrInvalidParams))
	}
	if params.Symbol == "" && params.Character == nil {
		return reply(ctx, nil, fmt.Errorf("%w: either symbol or character (via file:line:col) is required", jsonrpc2.ErrInvalidParams))
	}

	sess, err := b.sessionForFile(ctx, params.File)
	if err != nil {
		return reply(ctx, nil, err)
	}

	// Form A: resolve symbol name to a position first.
	if params.Symbol != "" {
		resolved, err := b.resolveNamedPosition(ctx, sess, params)
		if err != nil {
			return reply(ctx, nil, err)
		}
		params = resolved
	}

	// Dispatch the positional request using the original method name.
	raw, err := json.Marshal(params)
	if err != nil {
		return reply(ctx, nil, err)
	}
	result, err := sess.Handle(ctx, req.Method(), raw)
	if err != nil {
		return reply(ctx, nil, err)
	}

	// If the LSP server returned null/empty for a definition request,
	// the cursor is already at the definition. Return the queried
	// position as the result so the caller gets a location instead of
	// an empty response.
	if req.Method() == DefinitionMethod && (len(result) == 0 || string(result) == "null") {
		selfLoc := []Location{{
			URI: "file://" + params.File,
			Range: Range{
				Start: Position{Line: params.Line - 1, Character: *params.Character - 1},
				End:   Position{Line: params.Line - 1, Character: *params.Character - 1},
			},
		}}
		result, _ = json.Marshal(selfLoc)
	}

	return reply(ctx, json.RawMessage(result), nil)
}

// handlePassthrough forwards the request directly to the session without
// position resolution. Used for non-position-taking operations like
// documentSymbol, workspaceSymbol, incomingCalls, outgoingCalls.
func (b *Broker) handlePassthrough(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	// Extract file from params for session routing. Different methods
	// carry the file in different places.
	var base struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(req.Params(), &base); err != nil {
		return reply(ctx, nil, fmt.Errorf("%w: %v", jsonrpc2.ErrInvalidParams, err))
	}

	// For workspace/symbol and call hierarchy, the file might be empty.
	// Use cwd as a fallback for session routing.
	file := base.File
	if file == "" {
		// Try to extract from nested item for call hierarchy.
		var itemParams struct {
			Item struct {
				URI string `json:"uri"`
			} `json:"item"`
		}
		if err := json.Unmarshal(req.Params(), &itemParams); err == nil && itemParams.Item.URI != "" {
			file = itemParams.Item.URI
		}
	}

	if file == "" {
		return reply(ctx, nil, fmt.Errorf("%w: cannot determine session (no file)", jsonrpc2.ErrInvalidParams))
	}

	sess, err := b.sessionForFile(ctx, file)
	if err != nil {
		return reply(ctx, nil, err)
	}

	result, err := sess.Handle(ctx, req.Method(), req.Params())
	if err != nil {
		return reply(ctx, nil, err)
	}
	return reply(ctx, json.RawMessage(result), nil)
}

// handleDiagnostics handles the lsp.diagnostics request.
// It returns diagnostics for a specific file or for the whole project.
func (b *Broker) handleDiagnostics(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	var params DiagnosticsParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return reply(ctx, nil, fmt.Errorf("%w: %v", jsonrpc2.ErrInvalidParams, err))
	}
	if params.File == "" && !params.Project {
		return reply(ctx, nil, fmt.Errorf("%w: either file or project=true is required", jsonrpc2.ErrInvalidParams))
	}

	if params.File != "" {
		uri := string(protocol.URIFromPath(params.File))
		diags := b.Diags.ForFile(uri)
		if diags == nil {
			diags = []protocol.Diagnostic{}
		}
		raw, err := json.Marshal(diags)
		if err != nil {
			return reply(ctx, nil, err)
		}
		return reply(ctx, json.RawMessage(raw), nil)
	}

	// Project-wide diagnostics.
	all := b.Diags.ForProject()
	if all == nil {
		all = map[string][]protocol.Diagnostic{}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return reply(ctx, nil, err)
	}
	return reply(ctx, json.RawMessage(raw), nil)
}

// handleSync handles the lsp.sync request. It finds the session for
// the given file, forces a re-sync, and clears stale diagnostics.
func (b *Broker) handleSync(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	var params SyncParams
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

	// Clear stale diagnostics before re-syncing.
	uri := string(protocol.URIFromPath(params.File))
	b.Diags.Clear(uri)

	if err := sess.Sync(ctx, params.File); err != nil {
		return reply(ctx, nil, fmt.Errorf("sync %s: %w", params.File, err))
	}

	return reply(ctx, nil, nil)
}

// resolveNamedPosition resolves a Form A (name-based) request to a Form B
// (positional) request by calling documentSymbol on the session and
// matching the symbol name.
func (b *Broker) resolveNamedPosition(ctx context.Context, sess Session, params DefinitionParams) (DefinitionParams, error) {
	// Call documentSymbol on the session.
	dsParams := DocumentSymbolParams{
		Version: params.Version,
		File:    params.File,
	}
	raw, err := json.Marshal(dsParams)
	if err != nil {
		return params, err
	}
	result, err := sess.Handle(ctx, DocumentSymbolMethod, raw)
	if err != nil {
		return params, err
	}

	var symbols []protocol.DocumentSymbol
	if err := json.Unmarshal(result, &symbols); err != nil {
		return params, fmt.Errorf("decode documentSymbol result: %w", err)
	}

	// Collect all candidates by walking the tree.
	type candidate struct {
		fullName string
		line     int // 1-based
		char     int // 1-based
	}
	var candidates []candidate
	var walk func(syms []protocol.DocumentSymbol, prefix string)
	walk = func(syms []protocol.DocumentSymbol, prefix string) {
		for _, sym := range syms {
			fullName := sym.Name
			if prefix != "" {
				fullName = prefix + "." + sym.Name
			}
			// Check if the full dotted name ends with the user's query.
			if strings.HasSuffix(fullName, params.Symbol) || sym.Name == params.Symbol {
				c := candidate{
					fullName: fullName,
					line:     int(sym.SelectionRange.Start.Line) + 1, // 0-based → 1-based
					char:     int(sym.SelectionRange.Start.Character) + 1,
				}
				candidates = append(candidates, c)
			}
			walk(sym.Children, fullName)
		}
	}
	walk(symbols, "")

	// If line is specified, narrow candidates to those on that line.
	if params.Line > 0 && len(candidates) > 1 {
		var narrowed []candidate
		for _, c := range candidates {
			if c.line == params.Line {
				narrowed = append(narrowed, c)
			}
		}
		if len(narrowed) > 0 {
			candidates = narrowed
		}
	}

	switch len(candidates) {
	case 0:
		return params, ErrSymbolNotFound
	case 1:
		c := candidates[0]
		return DefinitionParams{
			Version:   params.Version,
			File:      params.File,
			Line:      c.line,
			Character: IntPtr(c.char),
		}, nil
	default:
		// Ambiguous — return error with candidate list.
		msg := fmt.Sprintf("ambiguous symbol %q: %d candidates", params.Symbol, len(candidates))
		for _, c := range candidates {
			msg += fmt.Sprintf("\n  %s at line %d", c.fullName, c.line)
		}
		return params, jsonrpc2.NewError(ErrCodeAmbiguousSymbol, msg)
	}
}

// sessionForFile returns the [Session] responsible for the given
// absolute file path, creating one if none exists.
//
// The routing logic:
//  1. If SessionOverride is set (tests), bypass all detection.
//  2. Find the project root via [FindProjectRoot].
//  3. Load .lsp.json config if present.
//  4. For Go-associated extensions (.go, .mod, .sum): use Go auto-config
//     unless .lsp.json explicitly claims the extension.
//  5. For other extensions: route via .lsp.json extensionToLanguage.
//  6. No match → [ErrNoServer].
func (b *Broker) sessionForFile(ctx context.Context, file string) (Session, error) {
	// Test override: bypass all config-aware routing.
	if b.SessionOverride != nil {
		root := filepath.Dir(file)
		return b.getOrCreateSession(ctx, root, "override", func() Session {
			return b.SessionOverride(root)
		})
	}

	root, err := FindProjectRoot(file)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(file)

	cfg, err := b.loadConfigCached(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("broker: load config for %s: %w", root, err)
	}

	// Trust check: if .lsp.json exists but the root is not trusted,
	// block non-Go requests. Go auto-config is safe (no user-controlled
	// commands) so it skips the trust check.
	if cfg != nil && b.TrustStore != nil && !b.TrustStore.IsTrusted(root) {
		// Go auto-config can still proceed for Go extensions.
		if !isGoExt(ext) {
			return nil, fmt.Errorf("%w: project root %s has .lsp.json but is not trusted; run: gopls lspcli trust add %s",
				ErrUntrustedRoot, root, root)
		}
		// For Go extensions, fall through to Go auto-config below
		// (which doesn't use .lsp.json).
		cfg = nil
	}

	// Go auto-config: .go/.mod/.sum files in a Go project.
	// Go auto-config wins unless .lsp.json explicitly claims the extension.
	if isGoExt(ext) {
		explicitOverride := false
		if cfg != nil {
			_, srv := cfg.ServerForExtension(ext)
			explicitOverride = srv != nil
		}
		if !explicitOverride {
			goRoot := FindGoRoot(filepath.Dir(file))
			if goRoot != "" {
				if b.GoSessionFactory == nil {
					return nil, ErrNoServer
				}
				return b.getOrCreateSession(ctx, goRoot, "go", func() Session {
					return b.GoSessionFactory(goRoot)
				})
			}
		}
	}

	// Config-based routing for all other extensions (or Go extensions
	// explicitly overridden in .lsp.json).
	if cfg != nil {
		serverID, serverCfg := cfg.ServerForExtension(ext)
		if serverCfg != nil {
			return b.getOrCreateSession(ctx, root, serverID, func() Session {
				return NewGenericSession(root, serverCfg, b.Diags, serverID)
			})
		}
	}

	return nil, ErrNoServer
}

// loadConfigCached returns the parsed .lsp.json for root, using a
// cached result when the file's mtime has not changed. If the mtime has
// changed (or this is the first call for root), stale sessions are
// evicted before the config is reloaded so that subsequent requests get
// a fresh session configured from the new file.
//
// Callers must NOT hold b.mu.
func (b *Broker) loadConfigCached(ctx context.Context, root string) (*Config, error) {
	cfgPath := filepath.Join(root, ".lsp.json")
	info, statErr := os.Stat(cfgPath)

	var mtime time.Time
	if statErr == nil {
		mtime = info.ModTime()
	}

	b.mu.Lock()
	entry, cached := b.configCache[root]
	b.mu.Unlock()

	// Fast path: config is cached and the file hasn't changed.
	if cached && entry.mtime.Equal(mtime) {
		return entry.cfg, nil
	}

	// Slow path: first load or mtime changed — evict stale sessions.
	if cached {
		b.mu.Lock()
		var toClose []Session
		for key, sess := range b.sessions {
			if key.root == root {
				toClose = append(toClose, sess)
				delete(b.sessions, key)
			}
		}
		b.mu.Unlock()
		// Close sessions without holding the lock; in-flight requests on
		// the old sessions complete normally (they hold their own reference).
		for _, sess := range toClose {
			if err := sess.Close(); err != nil {
				event.Error(ctx, "broker: evict session on config reload", err)
			}
		}
	}

	// Reload (or clear) the config.
	var cfg *Config
	if statErr == nil {
		var err error
		cfg, err = LoadConfig(root)
		if err != nil {
			return nil, err
		}
	}

	b.mu.Lock()
	b.configCache[root] = configEntry{cfg: cfg, mtime: mtime}
	b.mu.Unlock()

	return cfg, nil
}

// diagWirable is the interface that GoSession satisfies to receive the
// broker's DiagStore. Using an interface avoids importing goadapter from
// the broker package (which would create an import cycle).
type diagWirable interface {
	SetDiagStore(ds interface {
		Update(uri string, version int32, serverID string, diags []protocol.Diagnostic)
	}, serverID string)
}

// getOrCreateSession returns an existing session for the given key, or
// creates one via the create function if none exists.
func (b *Broker) getOrCreateSession(ctx context.Context, root, serverID string, create func() Session) (Session, error) {
	key := sessionKey{root: root, serverID: serverID}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return nil, fmt.Errorf("broker is stopped")
	}
	if s, ok := b.sessions[key]; ok {
		return s, nil
	}
	s := create()
	// Wire diagnostics store into sessions that support it (GoSession).
	if w, ok := s.(diagWirable); ok {
		w.SetDiagStore(b.Diags, serverID)
	}
	b.sessions[key] = s
	event.Log(ctx, fmt.Sprintf("broker: new session root=%s server=%s", root, serverID))
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

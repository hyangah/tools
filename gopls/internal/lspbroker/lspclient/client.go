// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lspclient provides a generic LSP client that communicates with a
// language server over stdio using the JSON-RPC 2.0 protocol (LSP header
// framing). It is independent of the broker and can be used standalone.
package lspclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/internal/jsonrpc2"
)

// State is the lifecycle state of a [Client].
type State int

const (
	// StateStopped means the client has not been started or has been shut down.
	StateStopped State = iota
	// StateStarting means the subprocess has been launched but initialize has
	// not completed.
	StateStarting
	// StateRunning means initialize succeeded and the client is ready.
	StateRunning
	// StateStopping means shutdown has been requested.
	StateStopping
	// StateErrored means the subprocess exited unexpectedly.
	StateErrored
)

// Config describes how to launch and initialize an LSP server.
type Config struct {
	// Command is the server command and arguments, e.g. ["gopls", "serve"].
	Command []string

	// Env contains extra environment variables for the subprocess. The current
	// process environment is always inherited first; Env values override it.
	Env []string

	// Dir is the working directory for the subprocess. If empty, the current
	// process working directory is inherited. Set this to the project root so
	// the LSP server does not inherit the daemon's cwd (which may be $HOME).
	Dir string

	// RootURI is the file:// URI of the project root directory.
	RootURI string

	// WorkspaceFolders is the set of workspace roots to advertise. If empty,
	// RootURI is used as the single folder.
	WorkspaceFolders []protocol.WorkspaceFolder

	// InitOptions is an optional JSON blob sent as InitializationOptions.
	InitOptions json.RawMessage

	// Settings is returned verbatim in response to workspace/configuration
	// requests from the server. May be nil.
	Settings json.RawMessage

	// StartupTimeout is the time budget for the initialize round-trip.
	// Defaults to 30 seconds.
	StartupTimeout time.Duration

	// Capabilities allows overriding the client capabilities advertised during
	// initialize. If nil, a sensible default is used.
	Capabilities *protocol.ClientCapabilities
}

// Client is a single LSP server connection. Create one with [Dial] or
// [dialConn] (for tests).
//
// A Client is safe for concurrent use.
type Client struct {
	cfg  Config
	cmd  *exec.Cmd // nil when created via dialConn
	conn jsonrpc2.Conn

	mu          sync.Mutex // protects state, err, diagFn, serverCaps, posEncoding
	state       State
	err         error
	diagFn      func(uri string, version int32, diags []protocol.Diagnostic)
	serverCaps  protocol.ServerCapabilities
	posEncoding protocol.PositionEncodingKind

	syncer syncer

	done chan struct{} // closed when the subprocess exits (or immediately if no subprocess)
}

// Dial launches the language server subprocess described by cfg.Command and
// completes the LSP initialize handshake. It returns a ready-to-use Client
// or an error if startup or initialize fails.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("lspclient.Dial: Config.Command must not be empty")
	}
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = 30 * time.Second
	}

	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lspclient.Dial: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lspclient.Dial: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // forward server log output

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lspclient.Dial: start %q: %w", cfg.Command[0], err)
	}

	// Bridge subprocess stdin/stdout to a net.Conn via net.Pipe so that
	// jsonrpc2.NewHeaderStream (which expects a net.Conn) can be used directly.
	local, remote := net.Pipe()
	go func() { io.Copy(stdin, remote); stdin.Close() }()   //nolint:errcheck
	go func() { io.Copy(remote, stdout); remote.Close() }() //nolint:errcheck

	return dialConn(ctx, cfg, local, cmd)
}

// dialConn constructs a Client over an already-established net.Conn. cmd may
// be nil; if non-nil its Wait is called when the connection is closed.
// This constructor is used by tests to inject an in-process fake server.
func dialConn(ctx context.Context, cfg Config, netConn net.Conn, cmd *exec.Cmd) (*Client, error) {
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = 30 * time.Second
	}

	stream := jsonrpc2.NewHeaderStream(netConn)
	jconn := jsonrpc2.NewConn(stream)

	c := &Client{
		cfg:         cfg,
		cmd:         cmd,
		conn:        jconn,
		state:       StateStarting,
		done:        make(chan struct{}),
		syncer:      newSyncer(),
		posEncoding: protocol.UTF16, // safe default until initialize response
	}

	// Start message dispatch.
	jconn.Go(ctx, jsonrpc2.MustReplyHandler(c.handle))

	// Watch for subprocess exit (or conn close if no subprocess).
	go c.watchExit()

	initCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()
	if err := c.initialize(initCtx); err != nil {
		jconn.Close()
		if cmd != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		return nil, fmt.Errorf("lspclient: initialize: %w", err)
	}

	c.mu.Lock()
	c.state = StateRunning
	c.mu.Unlock()

	return c, nil
}

// State returns the current lifecycle state of the client.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Err returns the last fatal error (process crash or initialize failure).
// It is nil while the client is running normally.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// OnDiagnostics registers a callback that is invoked on every
// textDocument/publishDiagnostics notification. Only one callback can be
// registered; subsequent calls replace the previous one.
func (c *Client) OnDiagnostics(fn func(uri string, version int32, diags []protocol.Diagnostic)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagFn = fn
}

// Shutdown sends LSP shutdown + exit to the server and waits for the
// subprocess to terminate (or ctx to expire).
func (c *Client) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.state == StateStopped || c.state == StateErrored {
		c.mu.Unlock()
		return nil
	}
	c.state = StateStopping
	c.mu.Unlock()

	// LSP shutdown: request then notify exit.
	_, shutErr := c.conn.Call(ctx, "shutdown", nil, nil)
	notifyErr := c.conn.Notify(ctx, "exit", nil)
	c.conn.Close()

	// Wait for subprocess exit.
	select {
	case <-c.done:
	case <-ctx.Done():
		if c.cmd != nil {
			c.cmd.Process.Kill() //nolint:errcheck
		}
		return ctx.Err()
	}

	if shutErr != nil {
		return fmt.Errorf("shutdown: %w", shutErr)
	}
	return notifyErr
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// initialize runs the LSP initialize + initialized handshake.
func (c *Client) initialize(ctx context.Context) error {
	caps := c.cfg.Capabilities
	if caps == nil {
		caps = defaultClientCapabilities()
	}

	folders := c.cfg.WorkspaceFolders
	if len(folders) == 0 && c.cfg.RootURI != "" {
		folders = []protocol.WorkspaceFolder{{
			URI:  c.cfg.RootURI,
			Name: "workspace",
		}}
	}

	params := &protocol.InitializeParams{
		XInitializeParams: protocol.XInitializeParams{
			ProcessID:             int32(os.Getpid()),
			RootURI:               protocol.DocumentURI(c.cfg.RootURI),
			Capabilities:          *caps,
			InitializationOptions: c.cfg.InitOptions,
		},
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: folders,
		},
	}

	var result protocol.InitializeResult
	if _, err := c.conn.Call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("initialize call: %w", err)
	}
	c.mu.Lock()
	c.serverCaps = result.Capabilities
	if result.Capabilities.PositionEncoding != nil {
		c.posEncoding = *result.Capabilities.PositionEncoding
	}
	c.mu.Unlock()

	return c.conn.Notify(ctx, "initialized", &protocol.InitializedParams{})
}

// handle dispatches incoming server-to-client requests and notifications.
func (c *Client) handle(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case "workspace/configuration":
		var params struct {
			Items []struct {
				ScopeURI string `json:"scopeUri,omitempty"`
				Section  string `json:"section,omitempty"`
			} `json:"items"`
		}
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("workspace/configuration: %w", err))
		}
		c.mu.Lock()
		settings := c.cfg.Settings
		c.mu.Unlock()
		// Return the configured settings for every requested item.
		items := make([]json.RawMessage, len(params.Items))
		for i := range items {
			if len(settings) > 0 {
				items[i] = settings
			} else {
				items[i] = json.RawMessage("null")
			}
		}
		return reply(ctx, items, nil)

	case "workspace/workspaceFolders":
		c.mu.Lock()
		folders := c.cfg.WorkspaceFolders
		c.mu.Unlock()
		return reply(ctx, folders, nil)

	case "textDocument/publishDiagnostics":
		var params struct {
			URI         protocol.DocumentURI  `json:"uri"`
			Version     int32                 `json:"version,omitempty"`
			Diagnostics []protocol.Diagnostic `json:"diagnostics"`
		}
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, nil) // ignore malformed notifications
		}
		c.mu.Lock()
		fn := c.diagFn
		c.mu.Unlock()
		if fn != nil {
			fn(string(params.URI), params.Version, params.Diagnostics)
		}
		return reply(ctx, nil, nil)

	case "workspace/applyEdit":
		// The broker is read-only; reject all edit requests.
		return reply(ctx, nil, fmt.Errorf("lspclient: workspace/applyEdit not supported (read-only client)"))

	case "client/registerCapability", "client/unregisterCapability":
		// Accept silently.
		return reply(ctx, nil, nil)

	case "window/showMessage", "window/logMessage", "$/progress",
		"window/showMessageRequest":
		// Log or ignore; do not block.
		return reply(ctx, nil, nil)

	default:
		// Unknown method: for notifications there's no reply needed.
		return reply(ctx, nil, fmt.Errorf("%w: %q", jsonrpc2.ErrMethodNotFound, req.Method()))
	}
}

// watchExit waits for the subprocess (or the conn done channel) to terminate
// and transitions the client to StateErrored.
func (c *Client) watchExit() {
	defer close(c.done)

	if c.cmd != nil {
		err := c.cmd.Wait()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.state == StateStopping {
			c.state = StateStopped
		} else if c.state != StateStopped {
			c.state = StateErrored
			if err != nil {
				c.err = fmt.Errorf("lsp server exited unexpectedly: %w", err)
			} else {
				c.err = fmt.Errorf("lsp server exited unexpectedly (status 0)")
			}
			c.conn.Close()
		}
		return
	}

	// No subprocess: watch the jsonrpc2 conn's done channel.
	<-c.conn.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateStopping {
		c.state = StateStopped
	} else if c.state != StateStopped {
		c.state = StateErrored
		if err := c.conn.Err(); err != nil {
			c.err = fmt.Errorf("lsp connection closed: %w", err)
		}
	}
}

// defaultClientCapabilities returns the minimal capabilities we advertise.
func defaultClientCapabilities() *protocol.ClientCapabilities {
	return &protocol.ClientCapabilities{
		General: &protocol.GeneralClientCapabilities{
			PositionEncodings: []protocol.PositionEncodingKind{
				protocol.UTF8,
				protocol.UTF16,
			},
		},
		TextDocument: protocol.TextDocumentClientCapabilities{
			Definition: &protocol.DefinitionClientCapabilities{
				LinkSupport: false,
			},
			DocumentSymbol: protocol.DocumentSymbolClientCapabilities{
				HierarchicalDocumentSymbolSupport: true,
			},
			Synchronization: &protocol.TextDocumentSyncClientCapabilities{
				DidSave: true,
			},
			PublishDiagnostics: protocol.PublishDiagnosticsClientCapabilities{
				VersionSupport: true,
			},
			Rename: &protocol.RenameClientCapabilities{
				PrepareSupport: true,
			},
		},
		Workspace: protocol.WorkspaceClientCapabilities{
			WorkspaceFolders: true,
			Configuration:    true,
		},
	}
}

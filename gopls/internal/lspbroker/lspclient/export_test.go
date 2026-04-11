// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspclient

import (
	"context"
	"net"
)

// DialConn is the test-only entry point that creates a Client over an
// existing net.Conn without spawning a subprocess.
func DialConn(ctx context.Context, cfg Config, netConn net.Conn) (*Client, error) {
	return dialConn(ctx, cfg, netConn, nil)
}

// Ping sends a $/ping call and waits for the response. It is used in tests to
// flush the notification queue — a successful Call guarantees that all
// preceding Notify messages have been processed by the server.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.conn.Call(ctx, "$/ping", nil, nil)
	return err
}

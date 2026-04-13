// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package goplscli

import (
	"context"
	"fmt"
)

// DefaultSocketAddress is not supported on this platform.
func DefaultSocketAddress() (string, error) {
	return "", fmt.Errorf("daemon auto-discovery is not supported on this platform")
}

// AutoConnect is not supported on this platform.
func AutoConnect(ctx context.Context) (string, error) {
	return "", fmt.Errorf("daemon auto-start is not supported on this platform")
}

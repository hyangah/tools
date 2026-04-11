// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package lspbroker

import "errors"

// Windows support is deferred to a future version. The broker daemon
// requires unix domain sockets and POSIX process signaling that are
// not yet implemented on Windows. See AGENTS.md#common-mistakes for
// guidance on keeping the code structured for future portability.

var errNotSupported = errors.New("lspbrokerd: not supported on Windows")

// AcquirePIDFile is not supported on Windows. It always returns an error.
func AcquirePIDFile(_ string) (cleanup func(), err error) {
	return nil, errNotSupported
}

func readPIDFile(_ string) (int, error) {
	return 0, errNotSupported
}

func isPIDAlive(_ int) bool {
	return false
}

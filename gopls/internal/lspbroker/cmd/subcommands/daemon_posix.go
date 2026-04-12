// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package subcommands

import "syscall"

// killProcess sends SIGTERM to the given process.
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

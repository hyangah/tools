// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// spawnDetached starts the broker daemon as a detached background
// process. It re-executes the binary at selfPath with the
// "lspbrokerd" subcommand and "--cache-dir cacheDir" arguments,
// using Setsid to detach the child from the controlling terminal.
// Stdin and stdout are set to /dev/null; stderr is redirected to
// cacheDir/broker.log so daemon messages are not lost.
func spawnDetached(selfPath, cacheDir string) error {
	logPath := filepath.Join(cacheDir, "broker.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		// If we can't open the log file, fall back to /dev/null.
		logFile, err = os.Open(os.DevNull)
		if err != nil {
			return fmt.Errorf("open daemon log %s: %w", logPath, err)
		}
	}
	defer logFile.Close()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()

	cmd := &syscall.ProcAttr{
		Files: []uintptr{
			devNull.Fd(), // stdin
			devNull.Fd(), // stdout
			logFile.Fd(), // stderr
		},
		Env: os.Environ(),
		Sys: &syscall.SysProcAttr{
			Setsid: true, // detach from the controlling terminal
		},
	}
	_, err = syscall.ForkExec(selfPath, []string{selfPath, "lspbrokerd", "--cache-dir", cacheDir}, cmd)
	if err != nil {
		return fmt.Errorf("forkexec %s: %w", selfPath, err)
	}
	return nil
}

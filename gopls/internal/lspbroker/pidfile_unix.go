// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package lspbroker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// AcquirePIDFile creates or opens the file at path, acquires an
// exclusive flock on it, writes the current process PID, and returns
// a cleanup function that releases the lock when called.
//
// If another process already holds the lock (i.e., a daemon is still
// starting up), AcquirePIDFile blocks until the lock is available.
// Callers should follow up by verifying broker.sock exists and is
// connectable before concluding the other daemon is healthy.
//
// The file is created with mode 0600.
func AcquirePIDFile(path string) (cleanup func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open pidfile %s: %w", path, err)
	}
	// Acquire exclusive lock. Blocks until any prior holder (e.g. a
	// daemon that is still starting) releases, which happens either on
	// normal exit or when the OS reclaims file descriptors at process
	// death.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock pidfile %s: %w", path, err)
	}
	// Overwrite with our PID.
	if err := f.Truncate(0); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, fmt.Errorf("truncate pidfile %s: %w", path, err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, fmt.Errorf("write pidfile %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// readPIDFile reads and parses the integer PID from the file at path.
// It returns an error if the file is missing, empty, or does not
// contain a valid positive integer.
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read pidfile %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("parse pidfile %s: invalid pid %q", path, strings.TrimSpace(string(data)))
	}
	return pid, nil
}

// isPIDAlive reports whether a process with the given pid is currently
// running. It uses kill(pid, 0) which checks process existence without
// delivering a signal.
func isPIDAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

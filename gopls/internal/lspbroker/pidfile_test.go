// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package lspbroker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePIDFile_WritesAndReleases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.pid")

	release, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("AcquirePIDFile: %v", err)
	}

	// PID file must contain our PID.
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("pidfile contains %d, want %d", pid, os.Getpid())
	}

	// Release the lock.
	release()

	// After release, a second AcquirePIDFile on the same path must
	// succeed immediately (non-blocking from the caller's perspective,
	// since no other goroutine holds the lock).
	release2, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("AcquirePIDFile after release: %v", err)
	}
	release2()
}

func TestIsPIDAlive_CurrentProcess(t *testing.T) {
	if !IsPIDAlive(os.Getpid()) {
		t.Error("IsPIDAlive returned false for the current process")
	}
}

func TestIsPIDAlive_InvalidPID(t *testing.T) {
	// PID 0 is never a valid user process; IsPIDAlive must return false.
	if IsPIDAlive(0) {
		t.Error("IsPIDAlive returned true for PID 0")
	}
}

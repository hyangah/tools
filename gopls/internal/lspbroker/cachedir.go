// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
)

// BuildIDHashLen is the number of hexadecimal characters of the
// build-id hash that are embedded in [CacheRoot]. Six characters give
// roughly 24 bits of entropy, which is sufficient to distinguish gopls
// builds on a single developer machine; matches the pattern used by
// gopls's own daemon forwarder in gopls/internal/lsprpc.
const BuildIDHashLen = 6

// BuildIDHash returns a short, stable identifier for the currently
// running gopls binary, used to isolate CLI/daemon pairs across gopls
// upgrades.
//
// It computes sha256 of the output of "go tool buildid" on the current
// executable, and returns the first [BuildIDHashLen] hex characters.
// If the buildid command fails (stripped binary, go not on $PATH,
// non-gopls callers, etc.), it falls back to sha256 of the absolute
// executable path so callers always receive a non-empty value.
//
// The pattern is borrowed from
// gopls/internal/lsprpc/autostart_posix.go#autoNetworkAddressPosix,
// which solves the same problem for gopls's own -remote=auto
// forwarder. See the project's ADR-005 for the rationale.
func BuildIDHash() string {
	self, err := os.Executable()
	if err != nil {
		// A truly pathological situation, but we must not crash the
		// CLI or daemon here. Return a stable sentinel so the caller
		// can still produce a path and diagnose separately.
		return "unknown"
	}
	return buildIDHashFor(self)
}

// buildIDHashFor is the testable core of [BuildIDHash]: it accepts an
// explicit binary path so tests can construct synthetic binaries and
// verify that distinct inputs produce distinct outputs.
//
// "go tool buildid" is tolerant of non-Go inputs — on such binaries
// it exits 0 and writes nothing but a trailing newline to stdout.
// Treating that as success would make two unrelated non-Go binaries
// collide on the same hash, so we require non-trivial output before
// accepting the buildid path.
func buildIDHashFor(binaryPath string) string {
	var out bytes.Buffer
	cmd := exec.Command("go", "tool", "buildid", binaryPath)
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		if id := bytes.TrimSpace(out.Bytes()); len(id) > 0 {
			sum := sha256.Sum256(id)
			return hex.EncodeToString(sum[:])[:BuildIDHashLen]
		}
	}
	// Fallback: hash the absolute path. Stable across runs of the
	// same binary at the same location; distinct across reinstalls
	// to different locations. Good enough when "go tool buildid"
	// isn't available (or returns nothing, as it does for non-Go
	// binaries).
	sum := sha256.Sum256([]byte(binaryPath))
	return hex.EncodeToString(sum[:])[:BuildIDHashLen]
}

// CacheRoot returns the directory under which all broker state
// (pidfile, socket, log) lives for the currently running gopls
// binary.
//
// The default is $XDG_CACHE_HOME/lsp-broker/<buildid>/, or
// $HOME/.cache/lsp-broker/<buildid>/ when XDG_CACHE_HOME is unset, or
// $TMPDIR/lsp-broker/<buildid>/ as a final fallback.
//
// Setting the environment variable LSP_BROKER_CACHE overrides the
// default and also disables the build-id subdirectory nesting; tests
// use it to get a fully private cache per run. See the project's
// designs/01-daemon-lifecycle.md for the path contract.
func CacheRoot() string {
	if d := os.Getenv("LSP_BROKER_CACHE"); d != "" {
		return d
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".cache")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "lsp-broker", BuildIDHash())
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package cmd

import "errors"

// spawnDetached is not supported on Windows. It always returns an error.
func spawnDetached(_, _ string) error {
	return errors.New("lspbrokerd --detach: not supported on Windows")
}

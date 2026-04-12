// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package subcommands

import "errors"

func killProcess(_ int) error {
	return errors.New("daemon stop: not supported on Windows")
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"sync"
	"time"
)

// idleTracker fires a callback when no request has been received for a
// configurable duration. Calling [idleTracker.bump] resets the timer.
// The callback is invoked at most once; it is the caller's
// responsibility to stop the broker after the callback fires.
//
// The zero value is not usable; call [newIdleTracker].
type idleTracker struct {
	mu      sync.Mutex
	timer   *time.Timer
	timeout time.Duration
	onIdle  func()
	fired   bool
}

// newIdleTracker creates an idleTracker that calls onIdle after
// timeout of inactivity. The timer starts immediately.
func newIdleTracker(timeout time.Duration, onIdle func()) *idleTracker {
	t := &idleTracker{
		timeout: timeout,
		onIdle:  onIdle,
	}
	t.timer = time.AfterFunc(timeout, t.fire)
	return t
}

// bump resets the idle timer. Call this whenever a request is received.
func (t *idleTracker) bump() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.fired {
		t.timer.Reset(t.timeout)
	}
}

// stop disarms the idle timer without firing the callback. Call this
// during graceful shutdown so the callback doesn't race with a
// caller-initiated stop.
func (t *idleTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer.Stop()
	t.fired = true // prevent a racing fire from invoking the callback
}

func (t *idleTracker) fire() {
	t.mu.Lock()
	if t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	fn := t.onIdle
	t.mu.Unlock()
	fn()
}

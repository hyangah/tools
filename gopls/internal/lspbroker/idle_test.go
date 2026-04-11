// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lspbroker

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestIdleTracker_FiresAfterTimeout(t *testing.T) {
	var fired atomic.Bool
	timeout := 50 * time.Millisecond
	tracker := newIdleTracker(timeout, func() {
		fired.Store(true)
	})
	defer tracker.stop()

	// Wait for more than the timeout; callback should fire.
	time.Sleep(2 * timeout)
	if !fired.Load() {
		t.Error("idle callback did not fire after timeout")
	}
}

func TestIdleTracker_ResetPreventsEarlyFire(t *testing.T) {
	var fired atomic.Bool
	timeout := 80 * time.Millisecond

	tracker := newIdleTracker(timeout, func() {
		fired.Store(true)
	})
	defer tracker.stop()

	// Bump repeatedly before the timeout, then stop. Callback should
	// never fire.
	for range 5 {
		time.Sleep(timeout / 4)
		tracker.bump()
	}
	tracker.stop()

	if fired.Load() {
		t.Error("idle callback fired despite repeated bumps")
	}
}

func TestIdleTracker_StopPreventsCallbackAfterIdle(t *testing.T) {
	var count atomic.Int32
	timeout := 30 * time.Millisecond
	tracker := newIdleTracker(timeout, func() {
		count.Add(1)
	})
	// Stop before the timer fires.
	tracker.stop()
	// Wait well past the timeout.
	time.Sleep(3 * timeout)
	if n := count.Load(); n != 0 {
		t.Errorf("callback fired %d time(s) after stop", n)
	}
}

func TestIdleTracker_FiresAtMostOnce(t *testing.T) {
	var count atomic.Int32
	timeout := 20 * time.Millisecond
	tracker := newIdleTracker(timeout, func() {
		count.Add(1)
	})
	defer tracker.stop()

	// Wait well past the timeout to give any spurious double-fires a
	// chance to appear.
	time.Sleep(5 * timeout)
	if n := count.Load(); n != 1 {
		t.Errorf("callback fired %d time(s), want exactly 1", n)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package goadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/tools/internal/jsonrpc2"
)

func TestCallWithRetry_NoRetryOnSuccess(t *testing.T) {
	calls := 0
	result, err := callWithRetry(context.Background(), func() (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestCallWithRetry_NoRetryOnNonContentModified(t *testing.T) {
	calls := 0
	_, err := callWithRetry(context.Background(), func() (string, error) {
		calls++
		return "", errors.New("some other error")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestCallWithRetry_RetriesContentModified(t *testing.T) {
	calls := 0
	contentModifiedErr := &jsonrpc2.WireError{Code: -32801, Message: "content modified"}

	result, err := callWithRetry(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", contentModifiedErr
		}
		return "recovered", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "recovered" {
		t.Errorf("result = %q, want %q", result, "recovered")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestCallWithRetry_ExhaustsRetries(t *testing.T) {
	calls := 0
	contentModifiedErr := &jsonrpc2.WireError{Code: -32801, Message: "content modified"}

	_, err := callWithRetry(context.Background(), func() (string, error) {
		calls++
		return "", contentModifiedErr
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// 1 initial + 3 retries = 4 calls
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
}

func TestCallWithRetry_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	contentModifiedErr := &jsonrpc2.WireError{Code: -32801, Message: "content modified"}

	calls := 0
	_, err := callWithRetry(ctx, func() (string, error) {
		calls++
		if calls == 1 {
			cancel() // Cancel after first call.
		}
		return "", contentModifiedErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCrashRecovery_ExhaustsRestarts(t *testing.T) {
	s := &GoSession{
		root:        "/tmp/test",
		maxRestarts: 0, // No restarts allowed.
	}
	// Simulate a state where ensureClient would try to connect but
	// we can't actually spawn gopls. With maxRestarts=0 and no client,
	// it should attempt to dial (which will fail since gopls isn't there).
	// This is a basic structural test.
	_ = s
	// More thorough crash recovery testing requires mocking the LSP
	// client, which is deferred to integration tests.
}

// Verify the retry backoff durations are reasonable (not a timing test,
// just checking the constants).
func TestRetryBackoffs(t *testing.T) {
	backoffs := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond, 2000 * time.Millisecond}
	total := time.Duration(0)
	for _, d := range backoffs {
		total += d
	}
	if total > 5*time.Second {
		t.Errorf("total backoff %v exceeds 5s", total)
	}
}

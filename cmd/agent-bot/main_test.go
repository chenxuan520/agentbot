package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
)

type fakeFeishuListenerFunc func(context.Context) error

func (f fakeFeishuListenerFunc) Listen(ctx context.Context) error {
	return f(ctx)
}

func TestRunFeishuListenerLoopRetriesTransientError(t *testing.T) {
	t.Parallel()

	cfg := config.Default(t.TempDir())
	cfg.FeishuAppID = "app-id"
	cfg.FeishuAppSecret = "app-secret"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	listener := fakeFeishuListenerFunc(func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary listener failure")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		done <- runFeishuListenerLoopWithRetry(ctx, cfg, listener, time.Millisecond, nil)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Fatalf("listener call count = %d, want at least 2", calls.Load())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runFeishuListenerLoopWithRetry: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runFeishuListenerLoopWithRetry did not exit after context cancellation")
	}
}

func TestRunFeishuListenerLoopFailsWithoutCredentials(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	err := runFeishuListenerLoopWithRetry(ctx, config.Default(t.TempDir()), fakeFeishuListenerFunc(func(ctx context.Context) error {
		calls.Add(1)
		return nil
	}), time.Millisecond, nil)
	if err == nil || err.Error() != "missing feishu app credentials" {
		t.Fatalf("error = %v, want missing feishu app credentials", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("listener call count = %d, want 0", calls.Load())
	}
}

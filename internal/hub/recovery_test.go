package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitOnlineWakesOnAttachAndBoundsWaiters(t *testing.T) {
	h := New()
	done := make(chan error, 1)
	go func() { done <- h.WaitOnline(context.Background(), "router", time.Second) }()
	select {
	case err := <-done:
		t.Fatal("returned before attachment", err)
	case <-time.After(10 * time.Millisecond):
	}
	conn, _ := tunnelPair(t, h)
	defer conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attachment did not wake request")
	}
	h.mu.Lock()
	h.waiting = 128
	h.mu.Unlock()
	if err := h.WaitOnline(context.Background(), "missing", time.Second); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
}

func TestWaitOnlineCancelsWithoutLeakingWaiter(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.WaitOnline(ctx, "router", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := h.WaitOnline(context.Background(), "router", time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.waiting != 0 {
		t.Fatal("leaked waiters", h.waiting)
	}
}

package hub

import (
	"context"
	"errors"
	"github.com/local/remotegate/internal/protocol"
	"testing"
	"time"
)

func TestQueuedRequestCanExpireWithoutWaitingForOtherWriter(t *testing.T) {
	h := New()
	_, _ = tunnelPair(t, h)
	h.mu.RLock()
	c := h.clients["router"]
	h.mu.RUnlock()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "queued"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request ignored its deadline while queued for writing")
	}
}

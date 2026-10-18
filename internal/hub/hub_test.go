package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

func tunnelPair(t *testing.T, h *Hub, remote ...bool) (*websocket.Conn, <-chan error) {
	t.Helper()
	ended := make(chan error, 1)
	attached := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := h.Attach("router", "test", "0.1.3", conn, false, len(remote) > 0 && remote[0])
		attached <- struct{}{}
		err = h.ReadLoop(c)
		h.Detach(c)
		_ = conn.Close()
		ended <- err
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("agent was not attached")
	}
	return conn, ended
}

func TestIdleTunnelStaysOnlineAndForwardsAfterProxyIdleWindow(t *testing.T) {
	h := New()
	h.pingInterval, h.pongWait = 25*time.Millisecond, 600*time.Millisecond
	conn, _ := tunnelPair(t, h)
	const idleWindow = 150 * time.Millisecond
	var pings atomic.Int32
	_ = conn.SetReadDeadline(time.Now().Add(idleWindow))
	conn.SetPingHandler(func(data string) error {
		pings.Add(1)
		// Simulate a proxy which closes the tunnel unless the server sends bytes.
		_ = conn.SetReadDeadline(time.Now().Add(idleWindow))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	go func() {
		defer conn.Close()
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: []byte("still available")}) != nil {
				return
			}
		}
	}()
	time.Sleep(3 * idleWindow)
	if pings.Load() < 2 || len(h.Statuses()) != 1 {
		t.Fatal("idle tunnel lost its connection", pings.Load(), h.Statuses())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "after-idle"})
	if err != nil || result.Status != 200 || string(result.Body) != "still available" {
		t.Fatal("request failed after idle period", result, err)
	}
}

func TestUnresponsiveTunnelGoesOfflineAndReleasesPendingRequest(t *testing.T) {
	h := New()
	h.pingInterval, h.pongWait = 20*time.Millisecond, 120*time.Millisecond
	conn, ended := tunnelPair(t, h)
	conn.SetPingHandler(func(string) error { return nil })
	go func() {
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "unanswered"})
	if errors.Is(err, context.DeadlineExceeded) || (err == nil && result.Error != "device disconnected") {
		t.Fatal("dead tunnel did not release the request", result, err)
	}
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("dead tunnel did not stop")
	}
	if len(h.Statuses()) != 0 {
		t.Fatal("dead tunnel still appears online")
	}
	if _, err := h.Request(ctx, "router", protocol.Message{ID: "offline"}); !errors.Is(err, ErrOffline) {
		t.Fatal("offline request was not rejected", err)
	}
}

func TestBusyLegacyAgentResponsesAlsoKeepTunnelAlive(t *testing.T) {
	h := New()
	h.pingInterval, h.pongWait = 20*time.Millisecond, 180*time.Millisecond
	conn, _ := tunnelPair(t, h)
	conn.SetPingHandler(func(string) error { return nil })
	go func() {
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200}) != nil {
				return
			}
		}
	}()
	for i := 0; i < 6; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "busy"})
		cancel()
		if err != nil || result.Status != 200 {
			t.Fatal("active legacy agent was disconnected", result, err)
		}
		time.Sleep(60 * time.Millisecond)
	}
}

func TestCancelledRequestDoesNotDisconnectHealthyTunnel(t *testing.T) {
	h := New()
	conn, _ := tunnelPair(t, h)
	go func() {
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			_ = conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200})
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request was sent", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "healthy"})
	if err != nil || result.Status != 200 {
		t.Fatal("cancellation closed the healthy tunnel", result, err)
	}
}

func TestConnectionPingWorksWithLegacyAgentAndConcurrentHTTPRequests(t *testing.T) {
	h := New()
	conn, _ := tunnelPair(t, h)
	go func() {
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200}) != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "during-ping"})
		result <- err
	}()
	if latency, err := h.Ping(ctx, "router"); err != nil || latency < 0 {
		t.Fatal(latency, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	status := h.Statuses()["router"]
	if status.LatencyMs == nil || status.LatencyAt.IsZero() || status.LastActiveAt.Before(status.Since) {
		t.Fatal("missing measured status", status)
	}
}

func TestPingTimeoutDoesNotDisconnectAndDisconnectReleasesPing(t *testing.T) {
	h := New()
	conn, _ := tunnelPair(t, h)
	seen := make(chan struct{}, 4)
	conn.SetPingHandler(func(string) error { seen <- struct{}{}; return nil })
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	if _, err := h.Ping(ctx, "router"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	if len(h.Statuses()) != 1 || h.Statuses()["router"].LatencyMs != nil {
		t.Fatal("timeout faked status")
	}
	<-seen
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ended := make(chan error, 1)
	go func() { _, err := h.Ping(ctx, "router"); ended <- err }()
	<-seen
	h.Disconnect("router")
	if err := <-ended; !errors.Is(err, ErrOffline) {
		t.Fatal("disconnect did not release ping", err)
	}
	if _, err := h.Ping(ctx, "router"); !errors.Is(err, ErrOffline) {
		t.Fatal("offline ping accepted", err)
	}
}

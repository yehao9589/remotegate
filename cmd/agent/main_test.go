package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

func TestHandlePreservesLoginRedirectAndCookie(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		http.Error(w, "redirect was followed", http.StatusForbidden)
	}))
	defer backend.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	got := handle(client, Config{AllowPublicTargets: true}, protocol.Message{Method: http.MethodPost, Target: backend.URL, Path: "/login"})
	if got.Status != http.StatusFound {
		t.Fatalf("status = %d, want %d", got.Status, http.StatusFound)
	}
	if got.Headers["Set-Cookie"][0] != "session=ok; Path=/" {
		t.Fatalf("cookie was not preserved: %#v", got.Headers)
	}
	if got.Headers["Location"][0] != "/admin" {
		t.Fatalf("location was not preserved: %#v", got.Headers)
	}
}

func TestReconnectDelayResetsAfterStableSessionAndCapsAtThirtySeconds(t *testing.T) {
	var delay time.Duration
	for _, seconds := range []int{1, 2, 4, 8, 16, 30, 30} {
		delay = nextReconnectDelay(delay, time.Second)
		if delay != time.Duration(seconds)*time.Second {
			t.Fatal("unexpected retry delay", delay, seconds)
		}
	}
	if got := nextReconnectDelay(delay, time.Minute); got != time.Second {
		t.Fatal("a stable connection must reset the next reconnect delay", got)
	}
}

func TestIdleAgentStopsWhenContextIsCancelled(t *testing.T) {
	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		if q := r.URL.Query(); q.Get("info") != "1" || q.Get("os") != runtime.GOOS || q.Get("arch") != runtime.GOARCH {
			t.Error("agent did not report actual platform")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connected <- struct{}{}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { ended <- run(ctx, Config{ServerURL: server.URL, DeviceID: "test", Token: "test"}) }()
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not connect")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("cancelled agent remained blocked on an idle socket")
	}
}

func TestSessionReportsActualHandshakeAndHeartbeat(t *testing.T) {
	allowPing := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		select {
		case <-allowPing:
		case <-time.After(5 * time.Second):
			return
		}
		if err := conn.WriteControl(websocket.PingMessage, []byte("probe"), time.Now().Add(time.Second)); err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{ServerURL: server.URL, DeviceID: "test", Token: "test-secret"}
	path := t.TempDir() + "/connection.json"
	status := newConnectionReporter(path, cfg)
	status.attempting()
	ended := make(chan error, 1)
	go func() { ended <- runSession(ctx, cfg, (&net.Dialer{}).DialContext, status) }()
	waitFor := func(heartbeat bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			status.mu.Lock()
			snapshot := status.state
			status.mu.Unlock()
			if snapshot.State == "connected" && snapshot.ConnectedAt != "" && (!heartbeat || snapshot.LastHeartbeatAt != "") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("Actual session did not report its handshake/heartbeat")
	}
	waitFor(false)
	close(allowPing)
	waitFor(true)
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("Session did not stop after cancellation")
	}
}

func TestCancelledAgentTargetRequestStopsPromptly(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan protocol.Message, 1)
	go func() {
		ended <- handleWithContext(ctx, http.DefaultClient, Config{}, protocol.Message{Method: "GET", Target: server.URL, Path: "/"})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("target request did not start")
	}
	cancel()
	select {
	case result := <-ended:
		if !strings.Contains(result.Error, "context canceled") {
			t.Fatal("target cancellation was not reported", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled agent remained blocked on the target")
	}
}

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
)

func TestMappedWebSocketRelaysMihomoHandshakeAndFrames(t *testing.T) {
	const secret = "fixture +/?&"
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		Subprotocols:      []string{"telemetry"},
		EnableCompression: true,
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = io.WriteString(w, `{"version":"fixture"}`)
			return
		}
		if r.URL.Path != "/traffic" || !websocket.IsWebSocketUpgrade(r) || r.URL.Query().Get("token") != secret {
			http.Error(w, "controller rejected secret", 401)
			return
		}
		if _, err := r.Cookie(mappingAccessCookie); err == nil {
			t.Error("RemoteGate access cookie reached the controller")
		}
		headers := http.Header{"Set-Cookie": {mappingAccessCookie + "=fixture; Path=/", "panel=fixture; Path=/"}}
		conn, err := upgrader.Upgrade(w, r, headers)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if conn.WriteMessage(websocket.TextMessage, []byte(`{"up":42}`)) != nil {
			return
		}
		for {
			kind, body, err := conn.ReadMessage()
			if err != nil || conn.WriteMessage(kind, body) != nil {
				return
			}
		}
	}))
	t.Cleanup(backend.Close)
	f := newAccessFixture(t)
	a := f.app
	f.mapping.Target = backend.URL
	if _, err := a.store.PutMapping(f.mapping); err != nil {
		t.Fatal(err)
	}
	const publicHost = "public.example.test"
	if _, err := a.store.PutMapping(config.Mapping{Host: publicHost, DeviceID: f.mapping.DeviceID, Target: backend.URL, AccessMode: "public", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	access := completeAccess(t, f)
	server := agentTestServer(t, a)
	attachMappedStreamFixture(t, a, server, f.mapping.DeviceID, f.token)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"telemetry"}, EnableCompression: true}
	dial := func(key string, headers http.Header) (*websocket.Conn, *http.Response, error) {
		return dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/traffic?token="+url.QueryEscape(key), headers)
	}
	deny := func(key string, headers http.Header, status int) {
		t.Helper()
		conn, resp, err := dial(key, headers)
		if conn != nil {
			conn.Close()
		}
		if resp != nil {
			defer resp.Body.Close()
		}
		if err == nil || resp == nil || resp.StatusCode != status || resp.Header.Get("Location") != "" {
			t.Fatalf("expected %d without redirect, got %v, %v", status, resp, err)
		}
	}
	protected := http.Header{"Host": {f.mapping.Host}, "X-Forwarded-Proto": {"https"}}
	deny(secret, protected, http.StatusUnauthorized)
	protected.Set("Cookie", access.Name+"="+access.Value)
	protected.Set("Origin", "https://foreign.example.com")
	deny(secret, protected, http.StatusForbidden)
	protected.Set("Origin", "https://"+f.mapping.Host)
	deny("wrong", protected, http.StatusUnauthorized)

	conn, resp, err := dial(secret, protected)
	if err != nil {
		t.Fatal(resp, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if conn.Subprotocol() != "telemetry" || len(resp.Cookies()) != 1 || resp.Cookies()[0].Name != "panel" {
		t.Fatal("handshake headers or cookie filtering changed", resp.Header)
	}
	if _, body, err := conn.ReadMessage(); err != nil || string(body) != `{"up":42}` {
		t.Fatal("initial telemetry did not arrive", string(body), err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if kind, body, err := conn.ReadMessage(); err != nil || kind != websocket.BinaryMessage || string(body) != "ping" {
		t.Fatal("bidirectional frame forwarding failed", kind, string(body), err)
	}

	public := http.Header{"Host": {publicHost}}
	for i := 1; i < protocol.MaxHTTPStreams/2; i++ {
		socket, response, err := dial(secret, public)
		if err != nil {
			t.Fatal(response, err)
		}
		defer socket.Close()
	}
	deny(secret, public, http.StatusServiceUnavailable)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/version", nil)
	req.Host = publicHost
	req.Header.Set("Authorization", "Bearer "+secret)
	client := &http.Client{Timeout: 5 * time.Second}
	version, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer version.Body.Close()
	if version.StatusCode != http.StatusOK {
		t.Fatal("WebSockets blocked ordinary HTTP requests", version.StatusCode)
	}

	_ = conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		reopened, response, err := dial(secret, public)
		if err == nil {
			reopened.Close()
			break
		}
		if response != nil {
			response.Body.Close()
		}
		if response == nil || response.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatal("closing the browser did not free its WebSocket slot", response, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteAccessHonorsDisableAndRequiresSafeTransport(t *testing.T) {
	for _, test := range []struct {
		url      string
		enabled  bool
		insecure bool
		want     bool
	}{
		{"https://console.example.com", false, false, false},
		{"https://console.example.com", true, false, true},
		{"https://console.example.com", true, true, false},
		{"http://console.example.com", true, false, false},
		{"http://127.0.0.1:18088", true, false, true},
		{"http://10.0.2.2:18088", true, false, true},
		{"http://localhost:18088", true, false, true},
	} {
		if got := remoteTransportAllowed(Config{ServerURL: test.url, RemoteAccess: test.enabled, InsecureTLS: test.insecure}); got != test.want {
			t.Fatal(test, got)
		}
	}
}

func TestRemoteAccessDefaultAndExplicitDisableSurviveConfigRoundTrip(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`{"serverUrl":"https://console.example.com"}`, true},
		{`{"serverUrl":"https://console.example.com","remoteAccess":false}`, false},
		{`{"serverUrl":"https://console.example.com","remoteAccess":true}`, true},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(test.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if remoteTransportAllowed(cfg) != test.want {
			t.Fatal("unexpected default", cfg)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var restored Config
		if err = json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.RemoteAccess != test.want {
			t.Fatal("saved setting changed after reload")
		}
	}
}

func TestConcurrentHTTPAssetBurstStaysQueued(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("asset"))
	}))
	defer target.Close()
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 24; i++ {
			if err = conn.WriteJSON(protocol.Message{Type: "request", ID: fmt.Sprint(i), Method: "GET", Target: target.URL, Path: "/asset"}); err != nil {
				result <- err
				return
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		seen := make(map[string]bool)
		for len(seen) < 24 {
			var reply protocol.Message
			if err = conn.ReadJSON(&reply); err != nil {
				result <- err
				return
			}
			if reply.Error != "" || reply.Status != 200 || string(reply.Body) != "asset" {
				result <- fmt.Errorf("asset request failed: %s", reply.Error)
				return
			}
			seen[reply.ID] = true
		}
		result <- nil
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Config{ServerURL: strings.TrimSuffix(server.URL, "/"), DeviceID: "fixture", Token: "fixture"})
	}()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

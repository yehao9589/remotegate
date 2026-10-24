package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
)

func recoveryFixture(t *testing.T) (*app, config.Device, string, config.Mapping) {
	t.Helper()
	s, err := config.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	d, token, err := s.CreateDevice("recovery")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMapping(config.Mapping{Host: "router.example.com", Target: "http://127.0.0.1:80", DeviceID: d.ID, Enabled: true, AccessMode: "public"})
	if err != nil {
		t.Fatal(err)
	}
	connected := time.Now().Add(-time.Minute)
	if err = s.RecordConnection(d.ID, connected, config.DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDisconnect(d.ID, connected, time.Now()); err != nil {
		t.Fatal(err)
	}
	return &app{store: s, hub: hub.New(), adminUser: "admin", adminPass: "admin"}, d, token, m
}

func TestMappingRecoveryWaitsBeforeSendingAndRevalidatesMapping(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "post_sent_once", true: "mapping_changed"}[changed], func(t *testing.T) {
			a, d, token, m := recoveryFixture(t)
			ts := agentTestServer(t, a)
			defer ts.Close()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", "http://router.example.com/save", strings.NewReader("one write"))
				w := httptest.NewRecorder()
				a.routes().ServeHTTP(w, r)
				done <- w
			}()
			// Confirm the request reached the bounded reconnect wait before changing anything.
			select {
			case w := <-done:
				t.Fatal("failed immediately", w.Code, w.Body.String())
			case <-time.After(30 * time.Millisecond):
			}
			if changed {
				m.Note = "updated while waiting"
				if _, err := a.store.PutMapping(m); err != nil {
					t.Fatal(err)
				}
			}
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/agent/connect?device_id="+d.ID+"&token="+token, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if !changed {
				var msg protocol.Message
				if err = conn.ReadJSON(&msg); err != nil {
					t.Fatal(err)
				}
				if msg.Method != "POST" || string(msg.Body) != "one write" {
					t.Fatal(msg)
				}
				if err = conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: []byte("saved")}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case w := <-done:
				if changed && w.Code != 409 || !changed && (w.Code != 200 || w.Body.String() != "saved") {
					t.Fatal(w.Code, w.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("request did not resume")
			}
			conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			if _, _, err = conn.ReadMessage(); err == nil {
				t.Fatal("request was replayed or stale mapping was used")
			}
		})
	}
}

func TestMappingRecoveryWindowAndSafeNavigation(t *testing.T) {
	a, d, _, _ := recoveryFixture(t)
	now := time.Now()
	if grace := a.mappingRecoveryGrace(d.ID, now); grace <= 0 || grace > 30*time.Second {
		t.Fatal(grace)
	}
	if a.mappingRecoveryGrace(d.ID, now.Add(time.Minute)) != 0 || a.mappingRecoveryGrace(d.ID, now.Add(-time.Minute)) != 0 {
		t.Fatal("invalid time received recovery grace")
	}
	for _, method := range []string{"GET", "POST", "HEAD"} {
		r := httptest.NewRequest(method, "http://router.example.com/", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		shown := a.mappingRecoveryPage(w, r, d.ID)
		if shown != (method == "GET") {
			t.Fatal(method, shown)
		}
		if shown {
			body, _ := io.ReadAll(w.Result().Body)
			if w.Code != 503 || !strings.Contains(string(body), "正在恢复连接") || !strings.Contains(string(body), "method:'HEAD'") {
				t.Fatal(w.Code, string(body))
			}
		}
	}
}

func TestMappingRecoveryInflightNavigationAndWriteAreNeverReplayed(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			a, d, token, _ := recoveryFixture(t)
			ts := agentTestServer(t, a)
			defer ts.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/agent/connect?device_id="+d.ID+"&token="+token, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest(method, "http://router.example.com/", strings.NewReader("one request"))
				r.Header.Set("Accept", "text/html")
				w := httptest.NewRecorder()
				a.routes().ServeHTTP(w, r)
				done <- w
			}()
			var msg protocol.Message
			if err = conn.ReadJSON(&msg); err != nil {
				t.Fatal(err)
			}
			if msg.Method != method {
				t.Fatal(msg)
			}
			conn.Close() // It may have executed the request, but no response arrived.
			select {
			case w := <-done:
				if method == "GET" {
					if w.Code != 503 || w.Header().Get("X-RemoteGate-Recovering") != "1" || !strings.Contains(w.Body.String(), "正在恢复连接") {
						t.Fatal(w.Code, w.Body.String())
					}
				}
				if method == "POST" {
					if w.Code != 502 || w.Header().Get("X-RemoteGate-Recovering") != "" {
						t.Fatal("uncertain write was made retryable", w.Code)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("in-flight request was queued for replay")
			}
		})
	}
}

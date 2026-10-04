package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
)

func TestHostRequestTravelsThroughAgent(t *testing.T) {
	store, _ := config.Open(t.TempDir() + "/state.json")
	d, token, err := store.CreateDevice("home")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// With no dedicated console host, a configured mapping still takes priority
	// for the root path. This is how local testing works before DNS is set up.
	a := &app{store: store, hub: hub.New(), adminUser: "admin", adminPass: "admin"}
	ts := httptest.NewServer(a.routes())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/agent/connect?device_id=" + d.ID + "&token=" + token + "&version=test"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request protocol.Message
		if conn.ReadJSON(&request) != nil {
			return
		}
		_ = conn.WriteJSON(protocol.Message{Type: "response", ID: request.ID, Status: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(request.Target + request.Path)})
	}()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "router.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), "http://127.0.0.1:80/"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	<-done
}

func TestAdminRequiresUsernameAndPassword(t *testing.T) {
	store, _ := config.Open(t.TempDir() + "/state.json")
	a := &app{store: store, hub: hub.New(), adminUser: "admin", adminPass: "admin"}
	ts := httptest.NewServer(a.routes())
	defer ts.Close()

	for _, tc := range []struct {
		name, user, pass string
		want             int
	}{
		{"valid credentials", "admin", "admin", http.StatusOK},
		{"wrong password", "admin", "bad", http.StatusUnauthorized},
		{"wrong username", "bad", "admin", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/state", nil)
			req.SetBasicAuth(tc.user, tc.pass)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("got status %d want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestRewriteInternalRedirect(t *testing.T) {
	headers := map[string][]string{"Location": {"http://127.0.0.1:80/admin"}}
	req := httptest.NewRequest(http.MethodGet, "http://router.example.com/", nil)
	req.Host = "router.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rewriteLocation(headers, "http://127.0.0.1:80", req)
	if got, want := headers["Location"][0], "https://router.example.com/admin"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEnrollmentNeedsWebsocket(t *testing.T) {
	store, _ := config.Open(t.TempDir() + "/state.json")
	d, token, _ := store.CreatePendingDevice("pending")
	a := &app{store: store, hub: hub.New()}
	ts := httptest.NewServer(a.routes())
	defer ts.Close()
	path := "/api/agent/connect?device_id=" + d.ID + "&token=" + token + "&version=0.1.0"
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(store.Snapshot().Devices) != 0 {
		t.Fatal("failed upgrade saved device")
	}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Ping waits for the server to finish activation before responding.
	if err = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && len(store.Snapshot().Devices) == 0; i++ {
		time.Sleep(time.Millisecond * 10)
	}
	got := store.Snapshot().Devices
	if len(got) != 1 || got[0].Version != "0.1.0" {
		t.Fatal("websocket did not activate/version device")
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/traffic"
)

func TestMappingTrafficIsCountedOnlyAfterAuthorizationAndReturnedToAdmin(t *testing.T) {
	f := newAccessFixture(t)
	totals := traffic.New()
	f.app.traffic = totals
	f.app.hub = hub.New(totals.Add)
	d, token, err := f.app.store.CreateDevice("traffic fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.mapping.DeviceID = d.ID
	f.mapping, err = f.app.store.PutMapping(f.mapping)
	if err != nil {
		t.Fatal(err)
	}
	ts := agentTestServer(t, f.app)
	defer ts.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/agent/connect?device_id="+d.ID+"&token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		var msg protocol.Message
		if conn.ReadJSON(&msg) == nil {
			conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: []byte("response-body")})
		}
	}()
	denied := httptest.NewRequest("POST", "https://router.example.com/upload", strings.NewReader("denied"))
	w := httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, denied)
	requireStatus(t, w, 401)
	if len(totals.Snapshot().Devices) != 0 {
		t.Fatal("authorization page counted as application traffic")
	}
	visitor := completeAccess(t, f)
	r := httptest.NewRequest("POST", "https://router.example.com/upload", strings.NewReader("uploaded"))
	r.AddCookie(visitor)
	r.Header.Set("Origin", "https://router.example.com")
	w = httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, r)
	requireStatus(t, w, 200)
	got := totals.Snapshot()
	if got.Mappings[f.mapping.ID].UploadBytes != 8 || got.Devices[d.ID].DownloadBytes != 13 {
		t.Fatal(got)
	}
	// Historical deleted IDs stay private to storage, never bloating the live admin snapshot.
	totals.Add("deleted-device", "deleted-mapping", 99, 99)
	state := authCall(f.app, "/api/admin/state", "", "", f.parent)
	requireStatus(t, state, 200)
	var snapshot struct {
		Traffic traffic.Snapshot `json:"traffic"`
	}
	if err = json.Unmarshal(state.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Traffic.Devices) != 2 || len(snapshot.Traffic.Mappings) != 1 || snapshot.Traffic.Mappings[f.mapping.ID].DownloadBytes != 13 {
		t.Fatal(snapshot)
	}
	requireStatus(t, authCall(f.app, "/api/admin/state", "", "", nil), http.StatusUnauthorized)
}

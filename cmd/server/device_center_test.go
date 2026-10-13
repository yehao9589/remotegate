package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

func waitDevice(t *testing.T, a *app, id string, online bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, connected := a.hub.Statuses()[id]
		d := a.store.Snapshot().Devices[0]
		if connected == online && ((!online && !d.LastDisconnectedAt.Before(d.LastConnectedAt)) || (online && !d.LastConnectedAt.IsZero())) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("device lifecycle did not settle", online, a.hub.Statuses(), a.store.Snapshot().Devices)
}

func TestDeviceManagementLegacyPingReconnectAndPreferences(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	device, token, err := a.store.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: device.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/devices/" + device.ID
	requireStatus(t, authCall(a, path+"/health", "{}", "", nil), 401)
	requireStatus(t, authCall(a, path+"/health", "{}", "https://evil.example", cookie), 403)
	requireStatus(t, authCall(a, path+"/health", "", "", cookie), 405)
	requireStatus(t, authCall(a, path+"/health", "{}", "", cookie), 409)
	requireStatus(t, authCall(a, "/api/admin/devices/missing/reconnect", "{}", "", cookie), 404)
	server := httptest.NewServer(a.routes())
	defer server.Close()
	connect := func(info bool) *websocket.Conn {
		query := url.Values{"device_id": {device.ID}, "token": {token}, "version": {"0.1.3"}, "package_version": {"0.1.3-1"}}
		if info {
			query.Set("info", "1")
			query.Set("os", "linux")
			query.Set("arch", "arm64")
			query.Set("hostname", "OpenWrt")
			query.Set("interface", "pppoe-wan")
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?"+query.Encode(), http.Header{"X-Real-IP": {"203.0.113.10"}})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				var msg protocol.Message
				if conn.ReadJSON(&msg) != nil {
					return
				}
				if conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: []byte("router still available")}) != nil {
					return
				}
			}
		}()
		waitDevice(t, a, device.ID, true)
		return conn
	}
	conn := connect(false)
	defer func() { conn.Close(); waitDevice(t, a, device.ID, false) }()
	ping := authCall(a, path+"/health", "{}", "", cookie)
	requireStatus(t, ping, 200)
	if !strings.Contains(ping.Body.String(), "latencyMs") {
		t.Fatal(ping.Body.String())
	}
	r := httptest.NewRequest(http.MethodPut, "http://console.localhost"+path, strings.NewReader(`{"note":"家里路由器","pinned":true}`))
	r.AddCookie(cookie)
	r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 204)
	state := authCall(a, "/api/admin/state", "", "", cookie)
	requireStatus(t, state, 200)
	if strings.Contains(state.Body.String(), device.TokenHash) || strings.Contains(state.Body.String(), token) {
		t.Fatal("device secret leaked")
	}
	var snapshot struct {
		Devices []struct {
			config.Device
			LatencyMs *int64 `json:"latencyMs"`
		}
	}
	if err := json.Unmarshal(state.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	got := snapshot.Devices[0]
	if !got.Pinned || got.Note != "家里路由器" || got.Name != "router" || got.Info.OS != "" || got.Info.PeerIP != "203.0.113.10" || got.LatencyMs == nil {
		t.Fatalf("bad device view: %+v", got)
	}
	a.deviceUpdates = map[string]pluginUpdateView{device.ID: {State: update.Status{Phase: "installing", StartedAt: time.Now().UTC()}}}
	requireStatus(t, authCall(a, path+"/reconnect", "{}", "", cookie), 409)
	// The update window may be closed: a session with the target package must
	// not remain locked by an old "restarting" record.
	a.deviceUpdates = map[string]pluginUpdateView{device.ID: {State: update.Status{Phase: "restarting", ToVersion: "0.1.3-1", StartedAt: time.Now().UTC()}}}
	requireStatus(t, authCall(a, path+"/reconnect", "{}", "", cookie), 200)
	waitDevice(t, a, device.ID, false)
	conn = connect(true)
	if _, ok := a.store.Authenticate(device.ID, token); !ok {
		t.Fatal("reconnect invalidated credentials")
	}
	gotDevice := a.store.Snapshot().Devices[0]
	if gotDevice.Info.OS != "linux" || gotDevice.Info.Arch != "arm64" || gotDevice.Info.Interface != "pppoe-wan" || !gotDevice.Pinned || gotDevice.Note != "家里路由器" {
		t.Fatal(gotDevice)
	}
	if got, ok := a.store.MappingForHost(mapping.Host); !ok || got.ID != mapping.ID {
		t.Fatal("reconnect changed mapping")
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	request.Host = mapping.Host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("mapping failed after reconnect", response.Status)
	}
}

func TestConnectionSourceTrustAndInformationLimits(t *testing.T) {
	for _, tc := range []struct{ remote, header, want string }{{"198.51.100.9:5555", "203.0.113.4", "198.51.100.9"}, {"127.0.0.1:5555", "203.0.113.4", "203.0.113.4"}, {"127.0.0.1:5555", "invalid", "127.0.0.1"}, {"[::1]:5555", "2001:db8::3", "2001:db8::3"}} {
		r := httptest.NewRequest("GET", "http://localhost/api/agent/connect?peerIP=192.0.2.5", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Real-IP", tc.header)
		if got := deviceConnectionInfo(r).PeerIP; got != tc.want {
			t.Fatal(tc, got)
		}
	}
	a := freshAuthApp(t)
	d, token, _ := a.store.CreatePendingDevice("pending")
	query := url.Values{"device_id": {d.ID}, "token": {token}, "info": {"1"}, "hostname": {strings.Repeat("x", 129)}}
	request := httptest.NewRequest("GET", "http://localhost/api/agent/connect?"+query.Encode(), nil)
	response := httptest.NewRecorder()
	a.connectAgent(response, request)
	if response.Code != 400 || len(a.store.Snapshot().Devices) != 0 {
		t.Fatal("invalid report activated device", response.Code)
	}
}

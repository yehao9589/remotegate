package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
)

func limitsCall(a *app, id, method, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://console.localhost/api/admin/devices/"+id+"/connection-limits", strings.NewReader(body))
	if a.auth != nil {
		r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func globalLimitsCall(a *app, method, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://console.localhost/api/admin/connection-limits", strings.NewReader(body))
	if a.auth != nil {
		r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestGlobalConnectionLimitsDefaultAndAllDevicesRequireAdministrator(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	admin := login.Result().Cookies()[0]
	one, token, _ := a.store.CreateDevice("one")
	two, _, _ := a.store.CreateDevice("offline other page")
	if err := a.store.SetDeviceConnectionLimits(two.ID, config.ConnectionLimits{WebSockets: 12, HTTPReserve: 4}); err != nil {
		t.Fatal(err)
	}
	createRegular(t, a, "member", []deviceGrant{{DeviceID: one.ID}})
	member := regularCookie(t, a, "member")
	const body = `{"limits":{"webSockets":24,"httpReserve":8},"applyToAll":true}`
	server := agentTestServer(t, a)
	attachMappedStreamFixture(t, a, server, one.ID, token, 128)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		requireStatus(t, globalLimitsCall(a, method, body, "", nil), 401)
		requireStatus(t, globalLimitsCall(a, method, body, "", member), 401)
	}
	requireStatus(t, globalLimitsCall(a, http.MethodPut, body, "https://foreign.example", admin), 403)
	requireStatus(t, globalLimitsCall(a, http.MethodPost, body, "", admin), 405)
	for _, bad := range []string{`{}`, `{"limits":{"webSockets":24}}`, `{"limits":{"webSockets":124,"httpReserve":8}}`, `{"limits":{"webSockets":24,"httpReserve":8},"applyToAll":"yes"}`, body + ` {}`, `{"limits":{"webSockets":24,"httpReserve":8},"deviceIds":[]}`} {
		requireStatus(t, globalLimitsCall(a, http.MethodPut, bad, "", admin), 400)
	}
	before := globalLimitsCall(a, http.MethodGet, "", "", admin)
	requireStatus(t, before, 200)
	var settings config.ConnectionLimitSettings
	if err := json.Unmarshal(before.Body.Bytes(), &settings); err != nil || settings.DeviceCount != 2 || settings.Defaults != config.DefaultConnectionLimits() {
		t.Fatal(settings, err)
	}
	onlyDefault := globalLimitsCall(a, http.MethodPut, `{"limits":{"webSockets":16,"httpReserve":8},"applyToAll":false}`, "", admin)
	requireStatus(t, onlyDefault, 200)
	if a.store.DeviceConnectionLimits(one.ID) != config.DefaultConnectionLimits() || a.store.DeviceConnectionLimits(two.ID) != (config.ConnectionLimits{WebSockets: 12, HTTPReserve: 4}) {
		t.Fatal("defaults modified existing devices")
	}
	if a.hub.Statuses()[one.ID].Connections.EffectiveWebSockets != 8 {
		t.Fatal("saving defaults changed the connected device's quota")
	}
	bulk := globalLimitsCall(a, http.MethodPut, body, "", admin)
	requireStatus(t, bulk, 200)
	if err := json.Unmarshal(bulk.Body.Bytes(), &settings); err != nil || settings.AppliedCount != 2 || settings.DeviceCount != 2 {
		t.Fatal(settings, err)
	}
	for _, id := range []string{one.ID, two.ID} {
		if a.store.DeviceConnectionLimits(id) != (config.ConnectionLimits{WebSockets: 24, HTTPReserve: 8}) {
			t.Fatal("bulk missed device", id)
		}
	}
	if a.hub.Statuses()[one.ID].Connections.EffectiveWebSockets != 24 {
		t.Fatal("bulk limits did not apply to the connected device")
	}
	if strings.Contains(bulk.Body.String(), token) || strings.Contains(bulk.Body.String(), one.TokenHash) {
		t.Fatal("global response exposed credentials")
	}
}

func TestDeviceConnectionLimitsAPIRequiresAdministratorAndPersists(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	admin := login.Result().Cookies()[0]
	d, token, _ := a.store.CreateDevice("router")
	createRegular(t, a, "member", []deviceGrant{{DeviceID: d.ID}})
	member := regularCookie(t, a, "member")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		requireStatus(t, limitsCall(a, d.ID, method, `{"webSockets":24,"httpReserve":8}`, "", nil), 401)
		requireStatus(t, limitsCall(a, d.ID, method, `{"webSockets":24,"httpReserve":8}`, "", member), 401)
	}
	requireStatus(t, limitsCall(a, d.ID, http.MethodPut, `{"webSockets":24,"httpReserve":8}`, "https://foreign.example", admin), 403)
	for _, invalid := range []string{`{}`, `{"webSockets":24}`, `{"webSockets":1.5,"httpReserve":8}`, `{"webSockets":125,"httpReserve":4}`, `{"webSockets":24,"httpReserve":8,"unknown":1}`, `{"webSockets":24,"httpReserve":8} {}`} {
		requireStatus(t, limitsCall(a, d.ID, http.MethodPut, invalid, "", admin), 400)
	}
	requireStatus(t, limitsCall(a, d.ID, http.MethodPost, `{}`, "", admin), 405)
	requireStatus(t, limitsCall(a, "missing", http.MethodGet, "", "", admin), 404)
	good := limitsCall(a, d.ID, http.MethodPut, `{"webSockets":24,"httpReserve":8}`, "", admin)
	requireStatus(t, good, 200)
	if strings.Contains(good.Body.String(), token) || strings.Contains(good.Body.String(), d.TokenHash) {
		t.Fatal("secret exposed")
	}
	var response struct {
		Limits      config.ConnectionLimits
		Connections hub.HTTPStreamUsage
	}
	if err := json.Unmarshal(good.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Limits != (config.ConnectionLimits{WebSockets: 24, HTTPReserve: 8}) || response.Connections.Capacity != 0 {
		t.Fatal(response)
	}
	reopened, err := config.Open(a.store.Directory() + "/state.json")
	if err != nil || reopened.DeviceConnectionLimits(d.ID) != response.Limits {
		t.Fatal("not persisted", err)
	}
	if _, valid := reopened.Authenticate(d.ID, token); !valid {
		t.Fatal("credentials changed")
	}
}

func TestMappedWebSocketLimitsCanGrowLiveAndReserveOrdinaryPages(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"version":"fixture"}`)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			kind, body, err := c.ReadMessage()
			if err != nil || c.WriteMessage(kind, body) != nil {
				return
			}
		}
	}))
	t.Cleanup(backend.Close)
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	admin := login.Result().Cookies()[0]
	d, token, _ := a.store.CreateDevice("router")
	const host = "limits.example.test"
	if _, err := a.store.PutMapping(config.Mapping{Host: host, DeviceID: d.ID, Target: backend.URL, AccessMode: "public", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	server := agentTestServer(t, a)
	attachMappedStreamFixture(t, a, server, d.ID, token, 128)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	dial := func() (*websocket.Conn, *http.Response, error) {
		return dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/traffic", http.Header{"Host": {host}})
	}
	var sockets []*websocket.Conn
	t.Cleanup(func() {
		for _, c := range sockets {
			c.Close()
		}
	})
	open := func() {
		t.Helper()
		c, resp, err := dial()
		if err != nil {
			t.Fatal(resp, err)
		}
		sockets = append(sockets, c)
	}
	busy := func() {
		t.Helper()
		c, resp, err := dial()
		if c != nil {
			c.Close()
		}
		if resp != nil {
			defer resp.Body.Close()
		}
		if err == nil || resp == nil || resp.StatusCode != 503 {
			t.Fatal("quota not enforced", resp, err)
		}
	}
	for i := 0; i < 8; i++ {
		open()
	}
	busy()
	requireStatus(t, limitsCall(a, d.ID, http.MethodPut, `{"webSockets":12,"httpReserve":8}`, "", admin), 200)
	for i := 8; i < 12; i++ {
		open()
	}
	busy()
	usage := a.hub.Statuses()[d.ID].Connections
	if usage.Capacity != 128 || usage.ActiveWebSockets != 12 || usage.EffectiveWebSockets != 12 || usage.EffectiveHTTPReserve != 8 {
		t.Fatal(usage)
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/version", nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("WebSockets blocked ordinary pages", resp.StatusCode)
	}
	requireStatus(t, limitsCall(a, d.ID, http.MethodPut, `{"webSockets":4,"httpReserve":8}`, "", admin), 200)
	busy()
	resp, err = (&http.Client{Timeout: 3 * time.Second}).Do(req.Clone(req.Context()))
	if err != nil {
		t.Fatal("lower quota blocked reserved HTTP capacity", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("lower quota blocked reserved HTTP capacity", resp.StatusCode)
	}
	if err := sockets[0].WriteMessage(websocket.TextMessage, []byte("still open")); err != nil {
		t.Fatal(err)
	}
	sockets[0].SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, body, err := sockets[0].ReadMessage(); err != nil || string(body) != "still open" {
		t.Fatal("lower limit interrupted an existing socket", err)
	}
	for _, c := range sockets {
		c.Close()
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.hub.Statuses()[d.ID].Connections.ActiveWebSockets != 0 {
		if time.Now().After(deadline) {
			t.Fatal("closed sockets retained quota")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 4; i++ {
		open()
	}
	busy()
}

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
)

func TestBrowserActivityHTMLIsGuardedBeforeApplicationScripts(t *testing.T) {
	a := freshAuthApp(t)
	d, _, _ := a.store.CreateDevice("router")
	m := config.Mapping{ID: "map", Host: "router.example.test", DeviceID: d.ID}
	page := `<!doctype html><html><head><meta charset="utf-8"><script src="app.js"></script></head><body>面板</body></html>`
	for _, compressed := range []bool{false, true} {
		raw := []byte(page)
		if compressed {
			var out bytes.Buffer
			gz := gzip.NewWriter(&out)
			gz.Write(raw)
			gz.Close()
			raw = out.Bytes()
		}
		header := http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Content-Length": {"999"}, "ETag": {"old"}, "Content-Security-Policy": {"script-src 'self'"}, "Set-Cookie": {"router=fixture"}}
		if compressed {
			header.Set("Content-Encoding", "gzip")
		}
		resp := &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(raw))}
		r := httptest.NewRequest("GET", "https://router.example.test/ui/metacubexd/", nil)
		r.Header.Set("Sec-Fetch-Dest", "document")
		a.prepareBrowserActivity(r, m, resp)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || !strings.Contains(string(body), `data-idle="900" data-background="60"`) || strings.Index(string(body), "guard.js") > strings.Index(string(body), "app.js") {
			t.Fatal("guard missing or installed after app", err, string(body))
		}
		if header.Get("Content-Length") != "" || header.Get("Content-Encoding") != "" || header.Get("ETag") != "" || header.Get("Cache-Control") != "no-store" {
			t.Fatal("stale transformed headers", header)
		}
		if header.Get("Content-Security-Policy") != "script-src 'self'" || header.Get("Set-Cookie") != "router=fixture" {
			t.Fatal("security or application cookie altered")
		}
	}
	if got := mappingProxyHeaders(httptest.NewRequest("GET", "http://router.example.test/", nil)); got["Accept-Encoding"] != nil {
		t.Fatal(got)
	}
}

func TestBrowserActivityDoesNotRewriteDownloadsJSONOrDisabledPages(t *testing.T) {
	a := freshAuthApp(t)
	d, _, _ := a.store.CreateDevice("router")
	m := config.Mapping{ID: "map", DeviceID: d.ID}
	for _, tc := range []struct {
		kind, disposition, encoding string
		disabled                    bool
	}{
		{"application/json", "", "", false}, {"text/html", "attachment; filename=page.html", "", false}, {"text/html", "", "br", false}, {"text/html", "", "", true},
	} {
		if tc.disabled {
			if err := a.store.SetDeviceConnectionLimits(d.ID, config.ConnectionLimits{WebSockets: 8, HTTPReserve: 8, IdleMinutes: -1, BackgroundMinutes: -1}); err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest("GET", "http://router.example.test/", nil)
		r.Header.Set("Accept", "text/html")
		header := http.Header{"Content-Type": {tc.kind}, "Content-Disposition": {tc.disposition}, "Content-Encoding": {tc.encoding}, "Content-Length": {"4"}}
		resp := &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader("data"))}
		a.prepareBrowserActivity(r, m, resp)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "data" || header.Get("Content-Length") != "4" {
			t.Fatal("unrelated response changed", tc, string(body), header)
		}
	}
}

func TestBrowserActivityPauseCannotBypassMappingLoginOrCrossOrigins(t *testing.T) {
	f := newAccessFixture(t)
	a := f.app
	token := a.newBrowserActivity(f.mapping)
	body := `{"token":"` + token + `","active":true}`
	request := func(origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://"+f.mapping.Host+browserActivityPath+"update", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Origin", origin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		return w
	}
	requireStatus(t, request("https://"+f.mapping.Host, nil), 401)
	cookie := completeAccess(t, f)
	requireStatus(t, request("https://foreign.example", cookie), 403)
	requireStatus(t, request("https://"+f.mapping.Host, cookie), 204)
	other := f.mapping
	other.ID = "other"
	if a.browserActivityValid(token, other) {
		t.Fatal("page lease crossed mapping")
	}
	key := sha256.Sum256([]byte(token))
	a.activityMu.Lock()
	item := a.activities[key]
	item.Touched = time.Now().Add(-16 * time.Minute)
	a.activities[key] = item
	a.activityMu.Unlock()
	requireStatus(t, request("https://"+f.mapping.Host, cookie), 410)
	if a.browserActivityValid(token, f.mapping) {
		t.Fatal("automatic request revived expired page")
	}
}

func TestPausedBrowserSocketClosesAndCannotAutomaticallyReconnect(t *testing.T) {
	var calls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RawQuery != "token=a%20b+%2F" {
			t.Error("gateway lease leaked or controller query changed", r.URL.RawQuery)
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.WriteMessage(websocket.TextMessage, []byte("telemetry"))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(backend.Close)
	a := freshAuthApp(t)
	a.activityPollInterval = 10 * time.Millisecond
	d, deviceToken, _ := a.store.CreateDevice("router")
	m, err := a.store.PutMapping(config.Mapping{Host: "idle.example.test", DeviceID: d.ID, Target: backend.URL, AccessMode: "public", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := agentTestServer(t, a)
	attachMappedStreamFixture(t, a, server, d.ID, deviceToken, 128)
	token := a.newBrowserActivity(m)
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/traffic?token=a%20b+%2F&" + browserActivityQuery + "=" + token
	dial := func() (*websocket.Conn, *http.Response, error) {
		return websocket.DefaultDialer.Dial(address, http.Header{"Host": {m.Host}})
	}
	c, resp, err := dial()
	if err != nil {
		t.Fatal(resp, err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	// Model a missing client pause beacon: server-side expiration still stops
	// a continuously transmitting stream, independent of network traffic.
	key := sha256.Sum256([]byte(token))
	a.activityMu.Lock()
	item := a.activities[key]
	item.HiddenSince = time.Now().Add(-2 * time.Minute)
	a.activities[key] = item
	a.activityMu.Unlock()
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("expired background page stayed connected")
	}
	for i := 0; i < 4; i++ {
		next, response, err := dial()
		if next != nil {
			next.Close()
		}
		if response != nil {
			response.Body.Close()
		}
		if err == nil || response == nil || response.StatusCode != 410 {
			t.Fatal("automatic reconnect was admitted", response, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("reconnects consumed device traffic", calls.Load())
	}
	other := a.newBrowserActivity(m)
	if !a.browserActivityValid(other, m) {
		t.Fatal("one paused page blocked another tab")
	}
}

func TestActivityPolicyDefaultsAndDisabledIntervals(t *testing.T) {
	now := time.Now()
	item := browserActivity{Touched: now.Add(-14 * time.Minute)}
	limits := config.DefaultConnectionLimits()
	if !activityLive(item, limits, now) || activityLive(item, limits, now.Add(2*time.Minute)) {
		t.Fatal("traffic should not extend idle time")
	}
	item.Touched = now
	item.HiddenSince = now.Add(-2 * time.Minute)
	if activityLive(item, limits, now) {
		t.Fatal("hidden page exceeded default grace")
	}
	limits.IdleMinutes = -1
	limits.BackgroundMinutes = -1
	if !activityLive(item, limits, now) {
		t.Fatal("explicitly disabled pause still applied")
	}
}

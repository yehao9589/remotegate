package main

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/tcpbridge"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMappedHTTPStreamFlushesPreservesLoginAndKeepsAccessProtection(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", Path: "/", HttpOnly: true})
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer backend.Close()
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	d, token, err := a.store.CreateDevice("stream fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []config.Mapping{{Host: "public.example.test", DeviceID: d.ID, Target: backend.URL, AccessMode: "public", Enabled: true}, {Host: "protected.example.test", DeviceID: d.ID, Target: backend.URL, AccessMode: "admin", Enabled: true}} {
		if _, err = a.store.PutMapping(m); err != nil {
			t.Fatal(err)
		}
	}
	server := agentTestServer(t, a)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?device_id="+d.ID+"&token="+token+"&http_stream=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var writer sync.Mutex
	bridge := tcpbridge.New(ctx, false, func(msg protocol.Message) error { writer.Lock(); defer writer.Unlock(); return ws.WriteJSON(msg) }, func(ctx context.Context, target string) (net.Conn, error) {
		u, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var msg protocol.Message
			if ws.ReadJSON(&msg) != nil {
				return
			}
			bridge.Handle(msg)
		}
	}()
	defer func() { cancel(); ws.Close(); bridge.Close(); <-done }()
	waitDevice(t, a, d.ID, true)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(host, path string, ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
		req.Host = host
		return client.Do(req)
	}
	result := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	requestCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		resp, err := request("public.example.test", "/", requestCtx)
		if err != nil {
			errs <- err
		} else {
			result <- resp
		}
	}()
	var resp *http.Response
	select {
	case resp = <-result:
	case err = <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("gateway buffered the full private response")
	}
	first := make([]byte, 5)
	if _, err = io.ReadFull(resp.Body, first); err != nil || string(first) != "first" {
		t.Fatal(err, string(first))
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("reverse proxy buffering was not disabled")
	}
	stop()
	resp.Body.Close()
	login, err := request("public.example.test", "/login", ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer login.Body.Close()
	if login.StatusCode != 302 || login.Header.Get("Location") != "/next" || len(login.Cookies()) != 1 || login.Cookies()[0].Value != "fixture" {
		t.Fatal("login redirect/cookie changed", login)
	}
	before := calls.Load()
	protected, err := request("protected.example.test", "/login", ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer protected.Body.Close()
	if calls.Load() != before || protected.StatusCode == 200 {
		t.Fatal("stream capability bypassed protected mapping authorization")
	}
}

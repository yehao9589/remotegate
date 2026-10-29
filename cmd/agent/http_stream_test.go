package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/traffic"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamingHTTPKeepsOtherMappingsAndHeartbeatResponsive(t *testing.T) {
	var closed atomic.Int32
	var mutations atomic.Int32
	var mutationClosed atomic.Int32
	abort := make(chan struct{})
	payload := bytes.Repeat([]byte("stream-data-"), 50000)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			w.Write([]byte("start"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				closed.Add(1)
			case <-abort:
			}
		case "/mutation":
			io.Copy(io.Discard, r.Body)
			mutations.Add(1)
			w.Write([]byte("started"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				mutationClosed.Add(1)
			case <-abort:
			}
		case "/bulk":
			w.Write(payload)
		case "/echo":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Write(data)
		default:
			w.Write([]byte("fast"))
		}
	}))
	defer target.Close()
	defer close(abort)
	totals := traffic.New()
	h := hub.New(totals.Add)
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := h.Attach("router", "", "", c, false, false, false, false, true, true)
		defer c.Close()
		defer h.Detach(client)
		_ = h.ReadLoop(client)
	}))
	defer tunnel.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() {
		ended <- run(ctx, Config{ServerURL: tunnel.URL, DeviceID: "fixture", Token: "fixture", RemoteAccess: false})
	}()
	defer func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Error("agent did not stop")
		}
	}()
	until := time.Now().Add(3 * time.Second)
	for !h.StreamingHTTP("router") && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if !h.StreamingHTTP("router") {
		t.Fatal("agent not online")
	}
	fetch := func(path, mapping string) (*http.Response, error) {
		return h.RoundTripHTTP(ctx, "router", protocol.Message{Method: "GET", Target: target.URL, Path: path}, mapping)
	}
	var slow []*http.Response
	defer func() {
		for _, resp := range slow {
			resp.Body.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		done := make(chan *http.Response, 1)
		errs := make(chan error, 1)
		go func() {
			resp, err := fetch("/slow", "slow")
			if err != nil {
				errs <- err
				return
			}
			done <- resp
		}()
		select {
		case resp := <-done:
			slow = append(slow, resp)
			first := make([]byte, 5)
			if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "start" {
				t.Fatal("first bytes were buffered", err)
			}
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatal("response header/first bytes waited for the complete body")
		}
	}
	fast := make(chan error, 1)
	go func() {
		resp, err := fetch("/fast", "fast")
		if err == nil {
			b, e := io.ReadAll(resp.Body)
			resp.Body.Close()
			if e != nil || string(b) != "fast" {
				err = errors.New("wrong fast response")
			}
		}
		fast <- err
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("four slow pages blocked another mapping")
	}
	if _, err := h.OpenTCP(ctx, "router", "127.0.0.1:22"); err == nil {
		t.Fatal("HTTP streaming enabled disabled SSH")
	}
	probe, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err := h.Ping(probe, "router"); err != nil {
		t.Fatal("heartbeat was blocked", err)
	}
	resp, err := fetch("/bulk", "bulk")
	if err != nil {
		t.Fatal(err)
	}
	var received bytes.Buffer
	buffer := make([]byte, 2048)
	for {
		n, e := resp.Body.Read(buffer)
		received.Write(buffer[:n])
		if n > 0 {
			time.Sleep(3 * time.Millisecond)
		} // about 5 Mbps, including reader overhead
		if e != nil {
			if e != io.EOF {
				t.Fatal(e)
			}
			break
		}
	}
	resp.Body.Close()
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatal("slow-reader flow control corrupted/truncated the response")
	}
	if got := totals.Snapshot().Mappings["bulk"].DownloadBytes; got != uint64(len(payload)) {
		t.Fatalf("stream headers/control were charged as body traffic: %d", got)
	}

	upload := bytes.Repeat([]byte("upload"), 50000)
	echo, err := h.RoundTripHTTP(ctx, "router", protocol.Message{Method: "POST", Target: target.URL, Path: "/echo", Body: upload}, "echo")
	if err != nil {
		t.Fatal(err)
	}
	echoed, err := io.ReadAll(echo.Body)
	echo.Body.Close()
	if err != nil || !bytes.Equal(upload, echoed) {
		t.Fatal("streamed upload corrupted", err)
	}
	if got := totals.Snapshot().Mappings["echo"]; got.UploadBytes != uint64(len(upload)) || got.DownloadBytes != uint64(len(upload)) {
		t.Fatal("HTTP body accounting differs", got)
	}
	changeCtx, stopChange := context.WithCancel(ctx)
	change, err := h.RoundTripHTTP(changeCtx, "router", protocol.Message{Method: "POST", Target: target.URL, Path: "/mutation", Body: []byte("write-once")}, "mutation")
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 7)
	if _, err = io.ReadFull(change.Body, prefix); err != nil {
		t.Fatal(err)
	}
	stopChange()
	change.Body.Close()
	deadline := time.Now().Add(time.Second)
	for mutationClosed.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if mutationClosed.Load() != 1 {
		t.Fatal("POST transport did not close after cancellation")
	}
	if mutations.Load() != 1 {
		t.Fatal("cancelled write was replayed", mutations.Load())
	}
	// Fill the independent HTTP quota. An extra request must expire without
	// dropping the tunnel; it may proceed after a browser releases one stream.
	for len(slow) < protocol.MaxHTTPStreams {
		resp, err := fetch("/slow", "slow")
		if err != nil {
			t.Fatal(err)
		}
		slow = append(slow, resp)
	}
	short, stopShort := context.WithTimeout(ctx, 70*time.Millisecond)
	_, err = h.RoundTripHTTP(short, "router", protocol.Message{Method: "GET", Target: target.URL, Path: "/fast"}, "quota")
	stopShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("HTTP quota ignored deadline", err)
	}
	slow[0].Body.Close()
	again, err := fetch("/fast", "fast-after-quota")
	if err != nil {
		t.Fatal("released HTTP slot did not recover", err)
	}
	io.Copy(io.Discard, again.Body)
	again.Body.Close()
	for _, resp := range slow {
		resp.Body.Close()
	}
	until = time.Now().Add(time.Second)
	for closed.Load() != protocol.MaxHTTPStreams && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if closed.Load() != protocol.MaxHTTPStreams {
		t.Fatal("closing browser bodies did not cancel private HTTP requests", closed.Load())
	}
}
func TestHTTPStreamTargetRestrictions(t *testing.T) {
	for _, target := range []string{"http://8.8.8.8", "ssh://127.0.0.1:22", "http://user:pass@127.0.0.1", "file:///etc/passwd"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := dialHTTPTarget(ctx, Config{}, target)
		cancel()
		if err == nil {
			conn.Close()
			t.Fatal("unsafe target accepted", target)
		}
	}
}
func TestLegacyCancelledAndExpiredRequestsReleaseAllWorkers(t *testing.T) {
	started := make(chan struct{}, 4)
	var expired atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/expired" {
			expired.Add(1)
		}
		w.Write([]byte("fast"))
	}))
	defer target.Close()
	result := make(chan error, 1)
	var serverWG sync.WaitGroup
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverWG.Add(1)
		defer serverWG.Done()
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		for _, id := range []string{"a", "b", "c", "d"} {
			if err = c.WriteJSON(protocol.Message{Type: "request", ID: id, Method: "GET", Target: target.URL, Path: "/slow"}); err != nil {
				result <- err
				return
			}
		}
		for i := 0; i < 4; i++ {
			select {
			case <-started:
			case <-time.After(time.Second):
				result <- errors.New("workers did not start")
				return
			}
		}
		c.WriteJSON(protocol.Message{Type: "request", ID: "expired", Method: "POST", Target: target.URL, Path: "/expired", Deadline: time.Now().Add(20 * time.Millisecond).UnixMilli()})
		time.Sleep(40 * time.Millisecond)
		for _, id := range []string{"a", "b", "c", "d"} {
			c.WriteJSON(protocol.Message{Type: "request-cancel", ID: id})
		}
		c.WriteJSON(protocol.Message{Type: "request", ID: "fast", Method: "GET", Target: target.URL, Path: "/fast"})
		c.SetReadDeadline(time.Now().Add(time.Second))
		for {
			var msg protocol.Message
			if err = c.ReadJSON(&msg); err != nil {
				result <- err
				return
			}
			if msg.ID == "fast" {
				if string(msg.Body) != "fast" {
					result <- errors.New("wrong fast response")
				} else {
					result <- nil
				}
				return
			}
		}
	}))
	defer tunnel.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { ended <- run(ctx, Config{ServerURL: tunnel.URL, DeviceID: "fixture", Token: "fixture"}) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled requests still blocked all mappings")
	}
	cancel()
	<-ended
	serverWG.Wait()
	if expired.Load() != 0 {
		t.Fatal("expired queued write reached the private service")
	}
}
func TestExpiredRequestNeverContactsTarget(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	got := handle(http.DefaultClient, Config{}, protocol.Message{Method: "POST", Target: target.URL, Path: "/", Deadline: time.Now().Add(-time.Second).UnixMilli()})
	if calls.Load() != 0 || !strings.Contains(got.Error, "deadline") {
		t.Fatal(got, calls.Load())
	}
}

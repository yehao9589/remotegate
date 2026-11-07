package tcpbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
)

func TestTCPBridgePrivateTargetsAndExplicitOptIn(t *testing.T) {
	for _, target := range []string{"8.8.8.8:22", "example.com:0", "127.0.0.1:65536", "127.0.0.1"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := dialPrivateTCP(ctx, target)
		cancel()
		if err == nil {
			conn.Close()
			t.Fatal("unacceptable target accepted", target)
		}
	}
	var reply protocol.Message
	bridge := New(context.Background(), false, func(msg protocol.Message) error { reply = msg; return nil })
	defer bridge.Close()
	bridge.Handle(protocol.Message{Type: "tcp-open", ID: "tcp:00000000000000000000000000000000", Target: "127.0.0.1:22"})
	if reply.Error == "" || len(bridge.streams) != 0 {
		t.Fatal("disabled bridge opened a connection")
	}
}

func TestHTTPBridgeCapacityIsBoundedAndReleased(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	replies := make(chan protocol.Message, 2*protocol.MaxHTTPStreamCapacity)
	var dials atomic.Int32
	var peersMu sync.Mutex
	var peers []net.Conn
	bridge := New(ctx, false, func(msg protocol.Message) error {
		replies <- msg
		return nil
	}, func(context.Context, string) (net.Conn, error) {
		conn, peer := net.Pipe()
		dials.Add(1)
		peersMu.Lock()
		peers = append(peers, peer)
		peersMu.Unlock()
		return conn, nil
	})
	t.Cleanup(func() {
		cancel()
		bridge.Close()
		peersMu.Lock()
		defer peersMu.Unlock()
		for _, peer := range peers {
			peer.Close()
		}
	})
	id := func(n int) string { return fmt.Sprintf("http:%032x", n) }
	open := func(n int) protocol.Message {
		t.Helper()
		if !bridge.Handle(protocol.Message{Type: "http-open", ID: id(n), Target: "http://127.0.0.1"}) {
			t.Fatal("HTTP stream was not handled")
		}
		select {
		case reply := <-replies:
			if reply.Type != "response" || reply.ID != id(n) {
				t.Fatalf("unexpected stream response: %#v", reply)
			}
			return reply
		case <-time.After(time.Second):
			t.Fatal("HTTP stream did not return an open result")
			return protocol.Message{}
		}
	}
	// The server chooses the quota; a newer agent must support more than the
	// legacy 16 streams while still enforcing an absolute allocation limit.
	for n := 0; n < protocol.MaxHTTPStreamCapacity; n++ {
		if reply := open(n); reply.Error != "" {
			t.Fatalf("stream %d was refused: %s", n, reply.Error)
		}
	}
	for n := protocol.MaxHTTPStreamCapacity; n < protocol.MaxHTTPStreamCapacity+16; n++ {
		if reply := open(n); reply.Status != http.StatusServiceUnavailable || reply.Error == "" {
			t.Fatalf("over-capacity stream was not refused: %#v", reply)
		}
	}
	if got := dials.Load(); got != int32(protocol.MaxHTTPStreamCapacity) {
		t.Fatalf("refused streams allocated target connections: %d", got)
	}
	bridge.mu.Lock()
	active := len(bridge.streams)
	bridge.mu.Unlock()
	if active != protocol.MaxHTTPStreamCapacity {
		t.Fatalf("unexpected active stream count: %d", active)
	}
	bridge.Handle(protocol.Message{Type: "tcp-close", ID: id(0)})
	if reply := open(protocol.MaxHTTPStreamCapacity + 16); reply.Error != "" {
		t.Fatalf("closed stream did not release its capacity: %s", reply.Error)
	}
	bridge.Close()
	bridge.mu.Lock()
	active = len(bridge.streams)
	bridge.mu.Unlock()
	if active != 0 {
		t.Fatalf("bridge shutdown left %d streams allocated", active)
	}
}

func TestTCPStreamsUseCreditsAndCoexistWithRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	h := hub.New()
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := h.Attach("device", "fixture", "new", conn, false, true)
		defer close(serverDone)
		defer conn.Close()
		defer h.Detach(client)
		_ = h.ReadLoop(client)
	}))
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var writer sync.Mutex
	send := func(msg protocol.Message) error { writer.Lock(); defer writer.Unlock(); return ws.WriteJSON(msg) }
	bridge := New(ctx, true, send)
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		for {
			var msg protocol.Message
			if ws.ReadJSON(&msg) != nil {
				return
			}
			if !bridge.Handle(msg) && msg.Type == "request" {
				_ = send(protocol.Message{Type: "response", ID: msg.ID, Body: []byte("mapping available"), Status: 200})
			}
		}
	}()
	defer func() { cancel(); ws.Close(); bridge.Close(); <-agentDone; <-serverDone }()
	deadline := time.Now().Add(time.Second)
	for len(h.Statuses()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	openCtx, stopOpen := context.WithTimeout(ctx, 10*time.Second)
	defer stopOpen()
	stream, err := h.OpenTCP(openCtx, "device", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	body := bytes.Repeat([]byte("UTF8-测试-0123456789\n"), 100000)
	written := make(chan error, 1)
	go func() { _, err := stream.Write(body); written <- err }()
	result, err := h.Request(openCtx, "device", protocol.Message{Type: "request", ID: "ordinary-mapping"})
	if err != nil || string(result.Body) != "mapping available" {
		t.Fatal("mapping blocked by TCP transfer", err, result)
	}
	got := make([]byte, len(body))
	if _, err = io.ReadFull(stream, got); err != nil {
		t.Fatal("credit-controlled transfer failed", err)
	}
	if err = <-written; err != nil || !bytes.Equal(body, got) {
		t.Fatal("TCP bytes changed", err)
	}
	var streams []net.Conn
	for i := 1; i < protocol.MaxDeviceStreams; i++ {
		extra, err := h.OpenTCP(openCtx, "device", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, extra)
		defer extra.Close()
	}
	if extra, err := h.OpenTCP(openCtx, "device", listener.Addr().String()); err == nil {
		extra.Close()
		t.Fatal("stream limit not enforced")
	}
	h.Disconnect("device")
	if _, err := streams[0].Read(make([]byte, 1)); err == nil {
		t.Fatal("disconnected stream remained readable")
	}
}

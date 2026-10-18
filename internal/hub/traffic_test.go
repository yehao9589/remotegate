package hub

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/traffic"
)

func TestTrafficAttributesActualHTTPBodiesAndExcludesControlMessages(t *testing.T) {
	totals := traffic.New()
	h := New(totals.Add)
	conn, _ := tunnelPair(t, h)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: []byte("下载")})
		}
	}()
	for i, kind := range []string{"request", "update-status", "request"} {
		id := []string{"one", "control", "two"}[i]
		if _, err := h.Request(context.Background(), "router", protocol.Message{Type: kind, ID: id, Body: []byte("abc")}, id); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	got := totals.Snapshot()
	if got.Devices["router"].UploadBytes != 6 || got.Devices["router"].DownloadBytes != 12 || got.Mappings["one"].DownloadBytes != 6 || got.Mappings["two"].UploadBytes != 3 || len(got.Mappings) != 2 {
		t.Fatal(got)
	}
	if _, err := h.Request(context.Background(), "offline", protocol.Message{Type: "request", ID: "offline", Body: []byte("not sent")}, "offline"); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	if len(totals.Snapshot().Devices) != 1 {
		t.Fatal("offline request counted")
	}
}

func TestTrafficKeepsUploadedBytesWhenResponseTimesOut(t *testing.T) {
	totals := traffic.New()
	h := New(totals.Add)
	conn, _ := tunnelPair(t, h)
	read := make(chan struct{})
	go func() { var msg protocol.Message; conn.ReadJSON(&msg); close(read) }()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := h.Request(ctx, "router", protocol.Message{Type: "request", ID: "slow", Body: []byte("sent")}, "slow")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-read
	if got := totals.Snapshot().Mappings["slow"]; got.UploadBytes != 4 || got.DownloadBytes != 0 {
		t.Fatal(got)
	}
}

func TestTrafficCountsTCPChannelOnceWithoutChargingAMapping(t *testing.T) {
	totals := traffic.New()
	h := New(totals.Add)
	conn, _ := tunnelPair(t, h, true)
	opened := make(chan string, 1)
	received := make(chan string, 1)
	go func() {
		var msg protocol.Message
		if conn.ReadJSON(&msg) != nil {
			return
		}
		opened <- msg.ID
		conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200})
		for {
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if msg.Type == "tcp-data" {
				received <- string(msg.Body)
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := h.OpenTCP(ctx, "router", "127.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	id := <-opened
	if _, err = stream.Write([]byte("upload")); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != "upload" {
		t.Fatal(got)
	}
	if err = conn.WriteJSON(protocol.Message{Type: "tcp-data", ID: id, Body: []byte("download")}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8)
	if _, err = io.ReadFull(stream, data); err != nil {
		t.Fatal(err)
	}
	got := totals.Snapshot()
	if got.Devices["router"].UploadBytes != 6 || got.Devices["router"].DownloadBytes != 8 || len(got.Mappings) != 0 {
		t.Fatal(got)
	}
}

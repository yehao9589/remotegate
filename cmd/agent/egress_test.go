package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

type fixtureEgressConn struct{ net.Conn }

func (fixtureEgressConn) EgressInterface() string { return "pppoe-wan" }

func TestSessionReportsSelectedSocketInterfaceNotConfiguration(t *testing.T) {
	reported := make(chan protocol.Message, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("interface") != "" {
			t.Error("automatic preference was changed")
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			t.Error(err)
			return
		}
		reported <- msg
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() {
		ended <- runSession(ctx, Config{ServerURL: server.URL, DeviceID: "fixture", Token: "fixture"}, func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if socketEgressInterface(conn) == "" {
				t.Error("ordinary local socket interface was not detected")
			}
			return fixtureEgressConn{conn}, nil
		}, nil)
	}()
	select {
	case msg := <-reported:
		if msg.Type != "device-network" || msg.EgressInterface != "pppoe-wan" {
			t.Fatal("wrong socket report", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing socket interface report")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not stop")
	}
}

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/local/remotegate/internal/protocol"
)

func TestTargetPathCannotReplaceConfiguredAuthority(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write([]byte("private")) }))
	defer target.Close()
	client := targetHTTPClient(Config{})
	defer client.CloseIdleConnections()
	for _, path := range []string{"//example.com/private", "https://example.com/private", "http://127.0.0.1/private", "relative", "/path#fragment"} {
		got := handle(client, Config{}, protocol.Message{Method: "GET", Target: target.URL, Path: path})
		if got.Error != "invalid request path" {
			t.Fatalf("accepted unsafe path %q: %+v", path, got)
		}
	}
	got := handle(client, Config{}, protocol.Message{Method: "GET", Target: target.URL, Path: "/valid?next=https://example.com/"})
	if got.Status != 200 || requests.Load() != 1 {
		t.Fatal(got, requests.Load())
	}
}

func TestPrivateHTTPDialerPinsCheckedDNSAddresses(t *testing.T) {
	for _, addresses := range [][]net.IPAddr{
		{{IP: net.ParseIP("192.168.1.1")}},
		{{IP: net.ParseIP("fe80::1"), Zone: "eth0"}},
	} {
		lookups, dials := 0, 0
		d := privateHTTPDialer{
			lookup: func(context.Context, string) ([]net.IPAddr, error) { lookups++; return addresses, nil },
			dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials++
				host, _, err := net.SplitHostPort(address)
				if err != nil || host == "changing.example" {
					t.Fatal("Dial resolved the hostname a second time", address)
				}
				if addresses[0].Zone != "" && host != "fe80::1%eth0" {
					t.Fatal("Lost IPv6 interface scope", host)
				}
				return nil, errors.New("fixture dial error")
			},
		}
		if _, err := d.DialContext(context.Background(), "tcp", "changing.example:80"); err == nil || lookups != 1 || dials != 1 {
			t.Fatal(err, lookups, dials)
		}
	}
	for _, addresses := range [][]net.IPAddr{nil, {{IP: net.ParseIP("8.8.8.8")}}, {{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("8.8.8.8")}}} {
		d := privateHTTPDialer{
			lookup: func(context.Context, string) ([]net.IPAddr, error) { return addresses, nil },
			dial: func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("Forbidden address reached dial")
				return nil, nil
			},
		}
		if _, err := d.DialContext(context.Background(), "tcp", "changing.example:80"); err == nil {
			t.Fatal("Forbidden DNS result accepted", addresses)
		}
	}
}

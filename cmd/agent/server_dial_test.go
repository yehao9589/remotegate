package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestServerDialerRecoversFakeIPAndCachesOnlyRealAnswers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	var lookups atomic.Int32
	d := &serverDialer{lookup: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("198.18.1.112")}}, nil
	}, realLookup: func(context.Context, string) ([]net.IPAddr, error) {
		lookups.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}, dial: (&net.Dialer{}).DialContext}
	for i := 0; i < 2; i++ {
		conn, err := d.DialContext(context.Background(), "tcp", "gate.example.com:"+port)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if lookups.Load() != 1 {
		t.Fatal("Real DNS answer was not cached", lookups.Load())
	}
	d.mu.Lock()
	d.cacheUntil = time.Now().Add(-time.Second)
	d.mu.Unlock()
	conn, err := d.DialContext(context.Background(), "tcp", "gate.example.com:"+port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if lookups.Load() != 2 {
		t.Fatal("Expired DNS answer was retained")
	}
	d.realLookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("198.19.1.2")}}, nil
	}
	d.mu.Lock()
	d.cacheUntil = time.Time{}
	d.mu.Unlock()
	if _, err = d.DialContext(context.Background(), "tcp", "gate.example.com:"+port); err == nil {
		t.Fatal("An encrypted fake address must never be dialled")
	}
}

func TestServerDialerKeepsLANNamesLocalAndRecoversPublicDNSFailure(t *testing.T) {
	var encrypted atomic.Int32
	d := &serverDialer{lookup: func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("system DNS unavailable") }, realLookup: func(context.Context, string) ([]net.IPAddr, error) {
		encrypted.Add(1)
		return nil, errors.New("fixture resolver unavailable")
	}}
	for _, host := range []string{"router", "router.lan", "router.local", "router.localhost", "router.example.test"} {
		_, _ = d.DialContext(context.Background(), "tcp", host+":443")
	}
	if encrypted.Load() != 0 {
		t.Fatal("Local names leaked to a public resolver")
	}
	_, err := d.DialContext(context.Background(), "tcp", "gate.example.com:443")
	if err == nil || encrypted.Load() != 1 || !strings.Contains(err.Error(), "加密 DNS") {
		t.Fatal(err, encrypted.Load())
	}
}

func TestNoDirectUplinkIsNotReportedAsDNSFailure(t *testing.T) {
	var attempts atomic.Int32
	dial := func(context.Context, string, string) (net.Conn, error) {
		attempts.Add(1)
		return nil, errNoDirectEgress
	}
	d := &serverDialer{
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("198.18.1.112")}}, nil
		},
		realLookup: func(ctx context.Context, host string) ([]net.IPAddr, error) { return lookupServerDoH(ctx, host, dial) },
	}
	_, err := d.DialContext(context.Background(), "tcp", "gate.example.com:443")
	if !errors.Is(err, errNoDirectEgress) || !strings.Contains(err.Error(), "WAN/LAN") || strings.Contains(err.Error(), "解析失败") {
		t.Fatal(err)
	}
	if attempts.Load() < 1 || attempts.Load() > 2 {
		t.Fatal("retried DNS endpoints despite missing uplink", attempts.Load())
	}
}

func TestServerDialPreservesHTTPSNameAndCertificateValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != "example.com" || !strings.HasPrefix(r.Host, "example.com:") {
			t.Error("Original TLS name or Host was lost", r.Host, r.TLS.ServerName)
		}
		io.WriteString(w, "connected")
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	d := &serverDialer{lookup: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("198.18.1.112")}}, nil
	}, realLookup: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}, dial: (&net.Dialer{}).DialContext}
	transport := &http.Transport{DialContext: d.DialContext, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get("https://example.com:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err = client.Get("https://mismatch.invalid:" + port + "/"); err == nil {
		t.Fatal("A mismatched certificate was accepted")
	}
}

func TestServerAddressRaceDoesNotWaitForBrokenAddressFamily(t *testing.T) {
	var closed atomic.Bool
	client, peer := net.Pipe()
	defer peer.Close()
	dial := func(ctx context.Context, _, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "[") {
			<-ctx.Done()
			closed.Store(true)
			return nil, ctx.Err()
		}
		return client, nil
	}
	started := time.Now()
	conn, err := dialServerAddresses(context.Background(), dial, "tcp", "443", []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}, {IP: net.ParseIP("192.0.2.1")}})
	if err != nil || time.Since(started) > time.Second {
		t.Fatal("Working IPv4 was blocked", err)
	}
	conn.Close()
	for i := 0; i < 100 && !closed.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !closed.Load() {
		t.Fatal("Losing connection was not cancelled")
	}
}

type dnsResponseTransport func(*http.Request) (*http.Response, error)

func (f dnsResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoHValidatesQuestionAndFollowsOnlyAnswerCNAMEs(t *testing.T) {
	for _, invalid := range []string{"", "case", "question", "id", "truncated", "oversize", "status"} {
		t.Run(invalid, func(t *testing.T) {
			client := &http.Client{Transport: dnsResponseTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "dns.alidns.com" || r.Header.Get("Authorization") != "" {
					t.Fatal("Unexpected resolver destination or credentials")
				}
				raw, _ := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
				var query dnsmessage.Message
				if err := query.Unpack(raw); err != nil {
					t.Fatal(err)
				}
				target := dnsmessage.MustNewName("alias.example.com.")
				unrelated := dnsmessage.MustNewName("other.example.com.")
				response := dnsmessage.Message{Header: dnsmessage.Header{Response: true, ID: query.ID}, Questions: query.Questions, Answers: []dnsmessage.Resource{
					{Header: dnsmessage.ResourceHeader{Name: query.Questions[0].Name, Class: dnsmessage.ClassINET}, Body: &dnsmessage.CNAMEResource{CNAME: target}},
					{Header: dnsmessage.ResourceHeader{Name: target, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}}},
					{Header: dnsmessage.ResourceHeader{Name: unrelated, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 2}}},
				}}
				switch invalid {
				case "case":
					response.Questions[0].Name = dnsmessage.MustNewName("GATE.EXAMPLE.COM.")
				case "question":
					response.Questions = []dnsmessage.Question{{Name: unrelated, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
				case "id":
					response.ID = 1
				case "truncated":
					response.Truncated = true
				}
				raw, err := response.Pack()
				if err != nil {
					t.Fatal(err)
				}
				if invalid == "oversize" {
					raw = bytes.Repeat([]byte{'x'}, 65536)
				}
				status := 200
				if invalid == "status" {
					status = 302
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			ips, err := queryServerDoH(context.Background(), client, "gate.example.com", dnsmessage.TypeA)
			if invalid != "" && invalid != "case" {
				if err == nil {
					t.Fatal("Invalid resolver response accepted", ips)
				}
				return
			}
			if err != nil || len(ips) != 1 || ips[0].IP.String() != "192.0.2.1" {
				t.Fatal(ips, err)
			}
		})
	}
}

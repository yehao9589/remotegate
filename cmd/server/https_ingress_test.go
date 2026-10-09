package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func ingressTestClient(cert, host string) (*http.Client, *http.Transport) {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(cert))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: host}, DisableKeepAlives: true}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, transport
}

func TestSharedHTTPSPreservesBaotaCertificateAndUsesManagedMappingCertificate(t *testing.T) {
	a, managedCert, _ := deploymentTestApp(t)
	domain := a.store.Domains()[0]
	domain.RootHTTPSProvider = "external"
	if _, err := a.store.PutDomain(domain); err != nil {
		t.Fatal(err)
	}
	device, _, err := a.store.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: device.ID, Target: "http://127.0.0.1:80", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	baotaCert, baotaKey := testCertificate(t, "example.com", time.Now().Add(24*time.Hour), "example.com", "other.example.org")
	pair, _, err := validateCertificate(baotaCert, baotaKey, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	baota := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("Baota " + r.Host + r.URL.RequestURI())) }))
	baota.TLS = &tls.Config{Certificates: []tls.Certificate{*pair}}
	baota.StartTLS()
	defer baota.Close()
	address := spareHTTPSAddress(t)
	settings := httpsSettings{Enabled: true, ListenAddress: address, Mode: "shared", FallbackAddress: baota.Listener.Addr().String()}
	if err = a.applyHTTPS(settings, true); err != nil {
		t.Fatal(err)
	}
	first := verifiedHTTPSLeaf(t, address, managedCert)
	for _, host := range []string{"example.com", "other.example.org"} {
		client, transport := ingressTestClient(baotaCert, host)
		request, _ := http.NewRequest("GET", "https://"+address+"/path?test=1", nil)
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("Baota passthrough failed", err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		transport.CloseIdleConnections()
		if string(body) != "Baota "+host+"/path?test=1" || response.TLS.PeerCertificates[0].SerialNumber.Cmp(pair.Leaf.SerialNumber) != 0 {
			t.Fatal("Baota request/certificate changed", string(body))
		}
	}
	cert2, key2 := testCertificate(t, "example.com", time.Now().Add(48*time.Hour))
	replacement, _, _ := validateCertificate(cert2, key2, "example.com")
	a.certs.mu.Lock()
	a.certs.pair = replacement
	a.certs.mu.Unlock()
	second := verifiedHTTPSLeaf(t, address, cert2)
	if first.SerialNumber.Cmp(second.SerialNumber) == 0 {
		t.Fatal("renewed mapping certificate not used on shared listener")
	}
	if got := a.httpsStatus(); got["mode"] != "shared" || got["address"] != address {
		t.Fatal(got)
	}
	// Saved sharing settings restore with the HTTP bootstrap listener still available.
	a.certMu.Lock()
	old := a.httpsServer
	a.httpsServer = nil
	a.httpsAddress = ""
	a.certMu.Unlock()
	old.Close()
	t.Setenv("HTTPS_LISTEN_ADDR", "")
	a.initializeHTTPS()
	if got := a.httpsStatus(); got["mode"] != "shared" || got["running"] != true || got["fallbackAddress"] != settings.FallbackAddress {
		t.Fatal("shared configuration did not restore", got)
	}
	verifiedHTTPSLeaf(t, address, cert2)
}

func TestSharedIngressOwnershipAndConfiguration(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	domain := a.store.Domains()[0]
	domain.RootHTTPSProvider = "external"
	a.store.PutDomain(domain)
	a.httpsSettings = httpsSettings{Mode: "shared", FallbackAddress: "127.0.0.1:9443"}
	for _, tc := range []struct {
		host   string
		native bool
	}{{"example.com", false}, {"router.example.com", true}, {"deep.router.example.com", false}, {"other.example.org", false}, {"", false}} {
		if choice := a.ingressRoute(tc.host); choice.native != tc.native {
			t.Fatal(tc.host, choice)
		}
	}
	device, _, _ := a.store.CreateDevice("offline")
	a.store.PutMapping(config.Mapping{Host: "deep.router.example.com", DeviceID: device.ID, Target: "http://127.0.0.1:80", Enabled: false})
	if !a.ingressRoute("deep.router.example.com").native {
		t.Fatal("disabled/uncovered mapping escaped to wrong Baota certificate")
	}
	for _, settings := range []httpsSettings{{ListenAddress: ":443", Mode: "shared", FallbackAddress: "192.0.2.1:9443"}, {ListenAddress: ":443", Mode: "shared", FallbackAddress: "localhost:9443"}, {ListenAddress: ":443", Mode: "shared", FallbackAddress: "127.0.0.1:443"}, {ListenAddress: ":443", Mode: "unknown"}} {
		if _, err := normalizeHTTPSSettings(settings); err == nil {
			t.Fatal("unsafe ingress configuration accepted", settings)
		}
	}
	if _, err := normalizeHTTPSSettings(httpsSettings{ListenAddress: ":443", Mode: "shared", FallbackAddress: "[::1]:9443", ProxyProtocol: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeHTTPSSettings(httpsSettings{ListenAddress: ":443"}); err != nil {
		t.Fatal("legacy direct configuration rejected", err)
	}
}

type fragmentReadConn struct{ net.Conn }

func (c fragmentReadConn) Read(p []byte) (int, error) {
	if len(p) > 7 {
		p = p[:7]
	}
	return c.Conn.Read(p)
}
func TestTLSHelloInspectionPreservesFragmentedOriginalBytes(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		handshake := tls.Client(client, &tls.Config{ServerName: "Router.Example.COM", MinVersion: tls.VersionTLS12})
		done <- handshake.Handshake()
	}()
	host, replay, err := inspectTLSHello(fragmentReadConn{server})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if host != "router.example.com" {
		t.Fatal(host)
	}
	buffer := make([]byte, replay.(*helloReplay).data.Len())
	if _, err = io.ReadFull(replay, buffer); err != nil {
		t.Fatal(err)
	}
	if len(buffer) < 5 || buffer[0] != 22 || !bytes.Contains(buffer, []byte("Router.Example.COM")) {
		t.Fatal("TLS replay lost ClientHello bytes")
	}
	server.Close()
	if err := <-done; err == nil {
		t.Fatal("inspection unexpectedly completed TLS handshake")
	}
}

func TestMalformedTLSDoesNotReachFallback(t *testing.T) {
	server, client := net.Pipe()
	go func() { client.Write([]byte("GET / HTTP/1.1\r\n\r\n")); client.Close() }()
	_, _, err := inspectTLSHello(server)
	server.Close()
	if err == nil {
		t.Fatal("plaintext request accepted as TLS ClientHello")
	}
}

func TestSharedIngressProxyProtocolAndShutdown(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ingress := newHTTPSIngress(public, func(string) ingressChoice {
		return ingressChoice{fallback: backend.Addr().String(), proxyProtocol: true}
	})
	defer ingress.Close()
	observed := make(chan string, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		line, _ := reader.ReadString('\n')
		record := make([]byte, 1)
		reader.Read(record)
		observed <- line + string(record)
	}()
	raw, err := net.Dial("tcp", public.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(raw, &tls.Config{ServerName: "other.example.org"})
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- client.Handshake() }()
	select {
	case header := <-observed:
		if !strings.HasPrefix(header, "PROXY TCP4 127.0.0.1 127.0.0.1 ") || !strings.HasSuffix(header, "\r\n\x16") {
			t.Fatal("missing PROXY header or altered TLS", header)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fallback handshake timed out")
	}
	ingress.Close()
	<-done
	if _, err := ingress.Accept(); err == nil {
		t.Fatal("closed ingress accepted a connection")
	}
}

func TestSharedFallbackPreflightFailurePreservesExistingListener(t *testing.T) {
	a, cert, _ := deploymentTestApp(t)
	address := spareHTTPSAddress(t)
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: address}, true); err != nil {
		t.Fatal(err)
	}
	settings := httpsSettings{Enabled: true, ListenAddress: address, Mode: "shared", FallbackAddress: spareHTTPSAddress(t)}
	if err := a.applyHTTPS(settings, true); err == nil {
		t.Fatal("non-listening Baota backend accepted")
	}
	if a.httpsSettings.Mode != "direct" {
		t.Fatal("failed preflight replaced existing ingress")
	}
	verifiedHTTPSLeaf(t, address, cert)
}

// A stop/rebind request can itself arrive through Baota. Closing the raw
// listener must allow that existing TLS tunnel to return its response.
func TestSharedStopThroughBaotaReturnsSettingsResponse(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	domain := a.store.Domains()[0]
	domain.RootHTTPSProvider = "external"
	if _, err := a.store.PutDomain(domain); err != nil {
		t.Fatal(err)
	}
	cert, key := testCertificate(t, "example.com", time.Now().Add(time.Hour), "example.com")
	pair, _, err := validateCertificate(cert, key, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	baota := httptest.NewUnstartedServer(a.routes())
	baota.TLS = &tls.Config{Certificates: []tls.Certificate{*pair}}
	baota.StartTLS()
	defer baota.Close()
	address := spareHTTPSAddress(t)
	settings := httpsSettings{Enabled: true, ListenAddress: address, Mode: "shared", FallbackAddress: baota.Listener.Addr().String()}
	if err = a.applyHTTPS(settings, true); err != nil {
		t.Fatal(err)
	}
	client, transport := ingressTestClient(cert, "example.com")
	defer transport.CloseIdleConnections()
	settings.Enabled = false
	body, _ := json.Marshal(settings)
	request, _ := http.NewRequest("POST", "https://"+address+"/api/admin/https", bytes.NewReader(body))
	request.Host = "example.com"
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("owner", "test-password-123")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("stop response lost through existing TLS tunnel", err)
	}
	defer response.Body.Close()
	var result map[string]any
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result["running"] != false {
		t.Fatal(response.StatusCode, result)
	}
}

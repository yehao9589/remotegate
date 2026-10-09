package main

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
)

func deploymentTestApp(t *testing.T) (*app, string, string) {
	t.Helper()
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	if err := a.store.SetDomainSettings(config.DomainSettings{BaseDomain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	a.httpsSettingsPath = filepath.Join(t.TempDir(), "https.json")
	m, err := newCertificateManager(filepath.Join(t.TempDir(), "cert.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.certs = m
	cert, key := testCertificate(t, "example.com", time.Now().Add(24*time.Hour))
	m.pair, _, err = validateCertificate(cert, key, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	m.state = certificateState{Domain: "example.com", Source: "upload", CertPEM: cert, KeyPEM: key, AccessKey: "private-dns-id", SecretKey: "private-dns-secret"}
	t.Cleanup(func() {
		a.certMu.Lock()
		server := a.httpsServer
		a.certMu.Unlock()
		if server != nil {
			server.Close()
		}
	})
	return a, cert, key
}

func spareHTTPSAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}

func verifiedHTTPSLeaf(t *testing.T, address, certificate string) *x509.Certificate {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certificate))
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "router.example.com", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

func TestSavedHTTPSListenerAndCertificateReplacement(t *testing.T) {
	a, cert, _ := deploymentTestApp(t)
	address := spareHTTPSAddress(t)
	settings := httpsSettings{Enabled: true, ListenAddress: address}
	if err := a.applyHTTPS(settings, true); err != nil {
		t.Fatal(err)
	}
	first := verifiedHTTPSLeaf(t, address, cert)
	server := a.httpsServer
	if err := a.applyHTTPS(settings, true); err != nil || a.httpsServer != server {
		t.Fatal("same listener should remain active", err)
	}
	cert2, key2 := testCertificate(t, "example.com", time.Now().Add(48*time.Hour))
	pair, _, _ := validateCertificate(cert2, key2, "example.com")
	a.certs.mu.Lock()
	a.certs.pair = pair
	a.certs.state.CertPEM = cert2
	a.certs.state.KeyPEM = key2
	a.certs.mu.Unlock()
	second := verifiedHTTPSLeaf(t, address, cert2)
	if first.SerialNumber.Cmp(second.SerialNumber) == 0 || a.httpsServer != server {
		t.Fatal("replacement was not served without restarting listener")
	}
	server.Close()
	a.certMu.Lock()
	a.httpsServer = nil
	a.httpsAddress = ""
	a.certMu.Unlock()
	t.Setenv("HTTPS_LISTEN_ADDR", "")
	a.initializeHTTPS()
	if a.httpsStatus()["running"] != true {
		t.Fatal("saved listener not restored", a.httpsStatus())
	}
	verifiedHTTPSLeaf(t, address, cert2)
}

func TestHTTPSReplacementFailurePreservesLiveService(t *testing.T) {
	a, cert, _ := deploymentTestApp(t)
	address := spareHTTPSAddress(t)
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: address}, true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.httpsSettingsPath)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: occupied.Addr().String()}, true); err == nil {
		t.Fatal("occupied port accepted")
	}
	after, _ := os.ReadFile(a.httpsSettingsPath)
	if !bytes.Equal(before, after) || a.httpsAddress != address {
		t.Fatal("failed update replaced persisted/live address")
	}
	verifiedHTTPSLeaf(t, address, cert)
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("not a directory"), 0600)
	a.httpsSettingsPath = filepath.Join(blocked, "https.json")
	newAddress := spareHTTPSAddress(t)
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: newAddress}, true); err == nil {
		t.Fatal("disk failure accepted")
	}
	if a.httpsAddress != address {
		t.Fatal("disk failure stopped old listener")
	}
	verifiedHTTPSLeaf(t, address, cert)
	released, err := net.Listen("tcp", newAddress)
	if err != nil {
		t.Fatal("failed replacement leaked listener", err)
	}
	released.Close()
}

func TestStoppingHTTPSOverItsOwnConnectionReturnsResponse(t *testing.T) {
	a, cert, _ := deploymentTestApp(t)
	address := spareHTTPSAddress(t)
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: address}, true); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(cert))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "router.example.com"}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	r, err := http.NewRequest("POST", "https://"+address+"/api/admin/https", strings.NewReader(`{"enabled":false,"listenAddress":"`+address+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.SetBasicAuth("owner", "test-password-123")
	r.Header.Set("Content-Type", "application/json")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal("listener closed before returning configuration response", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(string(raw), `"running":false`) {
		t.Fatal(response.StatusCode, string(raw))
	}
}

func TestNativeHTTPSMappingReachesConnectedDevice(t *testing.T) {
	for _, mode := range []string{"direct", "shared"} {
		t.Run(mode, func(t *testing.T) { testHTTPSMappingReachesConnectedDevice(t, mode) })
	}
}

func testHTTPSMappingReachesConnectedDevice(t *testing.T, mode string) {
	a, cert, _ := deploymentTestApp(t)
	device, token, err := a.store.CreateDevice("test router")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: device.ID, Target: "http://127.0.0.1:8080", PublicScheme: "https", PublicPort: 8443, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(a.routes())
	defer httpServer.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/agent/connect?device_id="+device.ID+"&token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	done := make(chan error, 1)
	go func() {
		var request protocol.Message
		err := connection.ReadJSON(&request)
		if err == nil {
			err = connection.WriteJSON(protocol.Message{Type: "response", ID: request.ID, Status: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(request.Target + request.Path)})
		}
		done <- err
	}()
	address := spareHTTPSAddress(t)
	settings := httpsSettings{Enabled: true, ListenAddress: address, Mode: mode}
	if mode == "shared" {
		baota := httptest.NewTLSServer(http.NotFoundHandler())
		defer baota.Close()
		settings.FallbackAddress = baota.Listener.Addr().String()
	}
	if err = a.applyHTTPS(settings, true); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(cert))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "router.example.com"}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, _ := http.NewRequest("GET", "https://"+address+"/admin/status?test=1", nil)
	request.Host = "router.example.com"
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != "http://127.0.0.1:8080/admin/status?test=1" {
		t.Fatal(response.StatusCode, string(body))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func deploymentRequest(a *app, method, path, body, target string, authenticated bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", target)
	if authenticated {
		r.SetBasicAuth("owner", "test-password-123")
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestHTTPSSettingsAuthorizationAndValidation(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	requireStatus(t, deploymentRequest(a, "GET", "/api/admin/https", "", "https://console.example.com", false), 401)
	requireStatus(t, deploymentRequest(a, "POST", "/api/admin/https", `{"enabled":true,"listenAddress":":0"}`, "https://console.example.com", true), 409)
	for _, address := range []string{":65536", "example.com:8443", "http://127.0.0.1:8443", ":-1", "8443"} {
		if _, err := normalizeHTTPSAddress(address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	for _, address := range []string{":443", "127.0.0.1:8443", "[::]:8443"} {
		if _, err := normalizeHTTPSAddress(address); err != nil {
			t.Fatalf("rejected %s: %v", address, err)
		}
	}
	address := spareHTTPSAddress(t)
	body, _ := json.Marshal(httpsSettings{Enabled: true, ListenAddress: address})
	requireStatus(t, deploymentRequest(a, "POST", "/api/admin/https", string(body), "https://console.example.com", true), 200)
	requireStatus(t, deploymentRequest(a, "POST", "/api/admin/https", `{"enabled":false,"listenAddress":"`+address+`"}`, "https://console.example.com", true), 200)
	if a.httpsStatus()["running"] != false {
		t.Fatal("listener still enabled")
	}
}

func TestCompleteCertificateDeploymentExport(t *testing.T) {
	a, cert, key := deploymentTestApp(t)
	path := "/api/admin/certificates"
	body := `{"action":"deploy-export","currentPassword":"test-password-123"}`
	requireStatus(t, deploymentRequest(a, "POST", path, body, "https://console.example.com", false), 401)
	requireStatus(t, deploymentRequest(a, "POST", path, `{"action":"deploy-export","currentPassword":"wrong"}`, "https://console.example.com", true), 400)
	w := deploymentRequest(a, "POST", path, body, "https://console.example.com", true)
	requireStatus(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("unsafe download headers")
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range archive.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r)
		r.Close()
		files[file.Name] = string(raw)
	}
	if len(files) != 4 || files["fullchain.pem"] != cert || files["privkey.pem"] != key {
		t.Fatal("missing or mismatched deployment certificate/key")
	}
	if _, _, err := validateCertificate(files["fullchain.pem"], files["privkey.pem"], "example.com"); err != nil {
		t.Fatal(err)
	}
	for _, content := range files {
		for _, secret := range []string{"private-dns-id", "private-dns-secret", "test-password-123"} {
			if strings.Contains(content, secret) {
				t.Fatal("unrelated credentials in deployment bundle")
			}
		}
	}
	if !strings.Contains(files["README.md"], "续期后请重新下载并导入") || !strings.Contains(files["proxy-headers.conf"], "Host $http_host") {
		t.Fatal("missing installation/renewal instructions")
	}
	for _, method := range []string{"GET", "POST"} {
		publicBody := ""
		if method == "POST" {
			publicBody = `{"action":"export"}`
		}
		w := deploymentRequest(a, method, path, publicBody, "https://console.example.com", true)
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "private-dns-secret") {
			t.Fatal("ordinary status/export exposed private material")
		}
	}
}

func TestDeploymentTransportAndReauthenticationLimits(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	body := `{"action":"deploy-export","currentPassword":"test-password-123"}`
	r := httptest.NewRequest("POST", "http://127.0.0.1:18088/api/admin/certificates", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:32000"
	r.Header.Set("Origin", "http://public.example.com")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	if privateDeploymentTransport(r) {
		t.Fatal("public HTTP reverse proxy accepted as local development")
	}
	r.Header.Set("Origin", "http://127.0.0.1:18088")
	r.Header.Del("X-Real-IP")
	if !privateDeploymentTransport(r) {
		t.Fatal("direct local development rejected")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	if !privateDeploymentTransport(r) {
		t.Fatal("trusted loopback HTTPS reverse proxy rejected")
	}
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Origin", "https://evil.example.org")
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("owner", "test-password-123")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 403)
	for i := 0; i < 5; i++ {
		requireStatus(t, deploymentRequest(a, "POST", "/api/admin/certificates", `{"action":"deploy-export","currentPassword":"wrong"}`, "https://console.example.com", true), 400)
	}
	requireStatus(t, deploymentRequest(a, "POST", "/api/admin/certificates", body, "https://console.example.com", true), 429)
}

func TestWildcardDeploymentDoesNotReplaceRootCertificate(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	cert, key := testCertificate(t, "example.com", time.Now().Add(24*time.Hour), "*.example.com")
	pair, _, err := validateCertificate(cert, key, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	a.certs.pair = pair
	a.certs.state.CertPEM = cert
	a.certs.state.KeyPEM = key
	address := spareHTTPSAddress(t)
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: address}, true); err != nil {
		t.Fatal(err)
	}
	verifiedHTTPSLeaf(t, address, cert)
	if _, err := a.selectHTTPSCertificate(&tls.ClientHelloInfo{ServerName: "example.com"}); err == nil {
		t.Fatal("wildcard served as a root-domain certificate")
	}
	w := deploymentRequest(a, "POST", "/api/admin/certificates", `{"action":"deploy-export","currentPassword":"test-password-123"}`, "https://console.example.com", true)
	requireStatus(t, w, 200)
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.File {
		if file.Name == "README.md" {
			reader, _ := file.Open()
			raw, _ := io.ReadAll(reader)
			reader.Close()
			found = strings.Contains(string(raw), "不要覆盖现有主域名站点的证书")
		}
	}
	if !found {
		t.Fatal("wildcard deployment omitted root-domain warning")
	}
}

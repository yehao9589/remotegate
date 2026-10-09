package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bootstrapTestEnv() map[string]string {
	return map[string]string{"BOOTSTRAP_DOMAIN": "example.com", "BOOTSTRAP_EMAIL": "owner@example.com", "BOOTSTRAP_DNS_PROVIDER": "alidns", "BOOTSTRAP_DNS_ACCESS_KEY": "private-dns-id", "BOOTSTRAP_DNS_SECRET_KEY": "private-dns-secret", "BOOTSTRAP_ACME_TERMS_ACCEPTED": "true"}
}

func TestBootstrapRequiresExplicitConsentAndValidConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"BOOTSTRAP_DOMAIN", "*.example.com"}, {"BOOTSTRAP_DOMAIN", "192.0.2.1"}, {"BOOTSTRAP_EMAIL", "bad"}, {"BOOTSTRAP_DNS_PROVIDER", "unsupported"}, {"BOOTSTRAP_DNS_ACCESS_KEY", ""}, {"BOOTSTRAP_DNS_SECRET_KEY", ""}, {"BOOTSTRAP_ACME_TERMS_ACCEPTED", "false"}} {
		env := bootstrapTestEnv()
		env[tc.key] = tc.value
		if _, enabled, err := bootstrapCertificateState(func(key string) string { return env[key] }); !enabled || err == nil {
			t.Fatal(tc, "invalid bootstrap accepted")
		}
	}
	env := bootstrapTestEnv()
	env["BOOTSTRAP_DNS_PROVIDER"] = "cloudflare"
	env["BOOTSTRAP_DNS_SECRET_KEY"] = ""
	state, enabled, err := bootstrapCertificateState(func(key string) string { return env[key] })
	if err != nil || !enabled || len(state.Names) != 1 || state.Names[0] != "example.com" || !state.AutoRenew {
		t.Fatal(enabled, err)
	}
	if _, enabled, err := bootstrapCertificateState(func(string) string { return "" }); enabled || err != nil {
		t.Fatal("empty bootstrap must be optional")
	}
}

func TestIndependentHTTPSFirstInstallAndSessionRestoreWithoutProxy(t *testing.T) {
	a := freshAuthApp(t)
	a.certDir = t.TempDir()
	a.certManagers = make(map[string]*certificateManager)
	a.httpsSettingsPath = filepath.Join(t.TempDir(), "https.json")
	m, err := a.certificateFor("example.com")
	if err != nil {
		t.Fatal(err)
	}
	cert, key := testCertificate(t, "example.com", time.Now().Add(24*time.Hour), "example.com")
	obtained := make(chan struct{})
	m.obtain = func(s certificateState) (string, string, error) {
		if len(s.Names) != 1 || s.Names[0] != "example.com" {
			t.Error("initial certificate should cover console only")
		}
		close(obtained)
		return cert, key, nil
	}
	address := spareHTTPSAddress(t)
	if err = a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: address}, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.httpsServer.Close() })
	env := bootstrapTestEnv()
	if err = a.bootstrapCertificate(func(key string) string { return env[key] }); err != nil {
		t.Fatal(err)
	}
	<-obtained
	deadline := time.Now().Add(5 * time.Second)
	for m.status("example.com")["running"] == true {
		if time.Now().After(deadline) {
			t.Fatal("bootstrap did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client, transport := ingressTestClient(cert, "example.com")
	defer transport.CloseIdleConnections()
	call := func(method, path, body string, cookie *http.Cookie) *http.Response {
		t.Helper()
		request, _ := http.NewRequest(method, "https://"+address+path, strings.NewReader(body))
		request.Host = "example.com"
		if method == "POST" {
			request.Header.Set("Origin", "https://example.com")
			request.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := call("GET", "/install", "", nil)
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(page), `<section id="firstInstall">`) {
		t.Fatal("independent TLS install page not available")
	}
	response = call("POST", "/api/auth/setup", `{"username":"owner","password":"test-password-123","confirmPassword":"test-password-123","entryPath":"/panel"}`, nil)
	response.Body.Close()
	cookies := response.Cookies()
	if response.StatusCode != 200 || len(cookies) != 1 || !cookies[0].Secure || cookies[0].Name != adminCookie {
		t.Fatal("independent HTTPS setup did not issue secure session", response.StatusCode, cookies)
	}
	for i := 0; i < 2; i++ {
		response = call("GET", "/panel", "", cookies[0])
		page, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !strings.Contains(string(page), `"panel":"workbench"`) {
			t.Fatal("independent reload lost session", response.StatusCode)
		}
	}
	response = call("GET", "/api/admin/certificates?domainId="+a.store.Domains()[0].ID, "", cookies[0])
	var status map[string]any
	err = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if err != nil || status["installed"] != true {
		t.Fatal(err, status)
	}
	if _, exists := status["accessKey"]; exists {
		t.Fatal("bootstrap DNS credential leaked")
	}
	if err = a.bootstrapCertificate(func(string) string { t.Error("environment read after initialization"); return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapPreservesInstalledCertificate(t *testing.T) {
	a, _, _ := deploymentTestApp(t)
	a.auth, _ = openAdminManager(filepath.Join(t.TempDir(), "admin.json"), "", "")
	if err := a.applyHTTPS(httpsSettings{Enabled: true, ListenAddress: spareHTTPSAddress(t)}, true); err != nil {
		t.Fatal(err)
	}
	m := a.certs
	m.mu.Lock()
	before := m.state
	m.mu.Unlock()
	env := bootstrapTestEnv()
	if err := a.bootstrapCertificate(func(key string) string { return env[key] }); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.CertPEM != before.CertPEM || m.state.AccessKey != before.AccessKey || m.running {
		t.Fatal("installed certificate replaced by bootstrap")
	}
}

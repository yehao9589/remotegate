package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func TestDomainChecksUseActualMappingEndpoints(t *testing.T) {
	d := config.DomainSettings{BaseDomain: "example.com"}
	targets, err := domainCheckTargets(d, []config.Mapping{
		{Host: "nas.example.com", PublicScheme: "https", PublicPort: 8443, Enabled: true},
		{Host: "old.example.com", Enabled: false},
		{Host: "other.test", Enabled: true},
	}, "admin.other.test", "", "")
	if err != nil || len(targets) != 2 || targets[0].Host != "nas.example.com" || targets[0].Port != 8443 || !targets[0].HTTPS || targets[1].Kind != "wildcard" {
		t.Fatalf("unexpected targets: %#v, %v", targets, err)
	}
	for _, tc := range []struct{ host, port string }{{"unrelated.test", "443"}, {"*.example.com", "443"}, {"nas.example.com", "70000"}, {"https://nas.example.com", "443"}} {
		if _, err := domainCheckTargets(d, nil, "", tc.host, tc.port); err == nil {
			t.Fatal("accepted invalid target", tc)
		}
	}
	targets, err = domainCheckTargets(d, nil, "", "nas.example.com", "8443")
	if err != nil || len(targets) != 1 || targets[0].Port != 8443 {
		t.Fatal(targets, err)
	}
}

func TestExternalRootHTTPSUsesConfiguredPublicPort(t *testing.T) {
	d := config.DomainSettings{BaseDomain: "example.com", RootHTTPSProvider: "external", RootHTTPSPort: 8443}
	targets, err := domainCheckTargets(d, nil, "", "", "")
	if err != nil || targets[0].Host != d.BaseDomain || targets[0].Port != 8443 || !targets[0].HTTPS {
		t.Fatal(targets, err)
	}
}

func TestCertificateRenewalControlsAndPublicExport(t *testing.T) {
	dir := t.TempDir()
	s, _ := config.Open(filepath.Join(dir, "state.json"))
	d, _ := s.PutDomain(config.DomainSettings{BaseDomain: "example.com"})
	m, _ := newCertificateManager(filepath.Join(dir, "certificate.json"))
	a := &app{store: s, certs: m}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/admin/certificates?domainId="+d.ID, strings.NewReader(string(raw)))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	cert, key := testCertificate(t, d.BaseDomain, time.Now().Add(80*24*time.Hour))
	if w := call(map[string]any{"action": "upload", "certificate": cert, "privateKey": key}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(map[string]any{"action": "renewal", "autoRenew": true}); w.Code != 400 || m.state.AutoRenew {
		t.Fatal("imported certificate accepted auto renewal")
	}
	if w := call(map[string]any{"action": "renew"}); w.Code != 400 {
		t.Fatal("imported certificate renewed")
	}
	w := call(map[string]any{"action": "export"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "CERTIFICATE") || strings.Contains(w.Body.String(), "PRIVATE KEY") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("export leaked private material", w.Code)
	}
	m.mu.Lock()
	m.state.Source = "acme"
	m.state.AccessKey = "secret-id"
	m.state.SecretKey = "secret-key"
	m.mu.Unlock()
	if w := call(map[string]any{"action": "renewal", "autoRenew": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reopened, err := newCertificateManager(m.path)
	if err != nil || !reopened.state.AutoRenew || len(reopened.state.History) != 2 {
		t.Fatal("renewal settings or history not persisted", err)
	}
	if strings.Contains(string(mustJSON(t, m.status(d.BaseDomain))), "secret-key") {
		t.Fatal("status leaked credentials")
	}
	if w := call(map[string]any{"action": "renewal", "autoRenew": false}); w.Code != 200 || m.state.AutoRenew {
		t.Fatal("failed to disable renewal")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestCertificateHistoryBounded(t *testing.T) {
	s := certificateState{}
	for i := 0; i < 50; i++ {
		addCertificateEvent(&s, "task", "success", "done")
	}
	if len(s.History) != 30 || s.History[0].Time.IsZero() {
		t.Fatal("history is not bounded")
	}
}

func TestCertificateTaskRestartRetainsInstalledPair(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "managed.json"))
	cert, key := testCertificate(t, "example.com", time.Now().Add(time.Hour))
	s := certificateState{Domain: "example.com", CertPEM: cert, KeyPEM: key}
	addCertificateEvent(&s, "申请开始", "running", "DNS 验证中")
	if err := m.saveLocked(s); err != nil {
		t.Fatal(err)
	}
	reopened, err := newCertificateManager(m.path)
	if err != nil || reopened.pair == nil || reopened.running || !strings.Contains(reopened.state.LastResult, "重启中断") {
		t.Fatal("restart recovery failed", err)
	}
}

func TestCertificateReloadReadsSavedPathsAndKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := config.Open(filepath.Join(dir, "state.json"))
	d, _ := s.PutDomain(config.DomainSettings{BaseDomain: "example.com"})
	m, _ := newCertificateManager(filepath.Join(dir, "managed.json"))
	a := &app{store: s, certs: m}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/admin/certificates?domainId="+d.ID, strings.NewReader(string(raw)))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	certPath, keyPath := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	cert, key := testCertificate(t, d.BaseDomain, time.Now().Add(time.Hour))
	os.WriteFile(certPath, []byte(cert), 0600)
	os.WriteFile(keyPath, []byte(key), 0600)
	if w := call(map[string]any{"action": "path", "certificatePath": certPath, "privateKeyPath": keyPath}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	newCert, newKey := testCertificate(t, d.BaseDomain, time.Now().Add(2*time.Hour))
	os.WriteFile(certPath, []byte(newCert), 0600)
	os.WriteFile(keyPath, []byte(newKey), 0600)
	if w := call(map[string]any{"action": "reload"}); w.Code != 200 || m.state.CertPEM != newCert {
		t.Fatal("reloaded old file", w.Body.String())
	}
	old := m.pair
	os.WriteFile(keyPath, []byte("bad"), 0600)
	if w := call(map[string]any{"action": "reload"}); w.Code != 400 || m.pair != old {
		t.Fatal("invalid reload replaced current certificate")
	}
}

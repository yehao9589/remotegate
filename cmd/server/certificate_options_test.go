package main

import (
	"crypto/tls"
	"encoding/json"
	"github.com/local/remotegate/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertificateScopeValidation(t *testing.T) {
	for _, bad := range []string{"outside.example", "evil-example.com", "https://example.com", "*.*.example.com", "a.example.com:443"} {
		if _, e := normalizeCertificateNames([]string{bad}, "example.com"); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	names, e := normalizeCertificateNames([]string{"EXAMPLE.com", "example.com", "*.lab.example.com", "router.example.com"}, "example.com")
	if e != nil || len(names) != 3 {
		t.Fatal(names, e)
	}
	c, k := testCertificate(t, "lab.example.com", time.Now().Add(time.Hour))
	pair, _, e := validateCertificate(c, k, "example.com")
	if e != nil {
		t.Fatal(e)
	}
	if e = verifyRequestedNames(pair, []string{"lab.example.com", "*.lab.example.com"}); e != nil {
		t.Fatal(e)
	}
	if e = verifyRequestedNames(pair, []string{"*.example.com"}); e == nil {
		t.Fatal("missing wildcard accepted")
	}
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "cert.json"))
	m.pair = pair
	m.state = certificateState{Domain: "example.com", Names: names}
	if _, e = m.getCertificate(&tls.ClientHelloInfo{ServerName: "app.lab.example.com"}); e != nil {
		t.Fatal(e)
	}
	if _, e = m.getCertificate(&tls.ClientHelloInfo{ServerName: "console.example.com"}); e == nil {
		t.Fatal("uncovered host accepted")
	}
	if !m.status("example.com")["certificate"].(map[string]any)["valid"].(bool) {
		t.Fatal("valid subdomain cert incorrectly rejected")
	}
}
func TestCAConfigurationAndProviderIsolation(t *testing.T) {
	dir := t.TempDir()
	store, _ := config.Open(filepath.Join(dir, "state.json"))
	store.SetDomainSettings(config.DomainSettings{BaseDomain: "example.com"})
	m, _ := newCertificateManager(filepath.Join(dir, "cert.json"))
	a := &app{store: store, certs: m}
	call := func(in map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
		r.RemoteAddr = "127.0.0.1:12"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	in := map[string]any{"action": "configure", "provider": "cloudflare", "accessKey": "dns-secret", "email": "test@example.com", "ca": "zerossl", "termsAccepted": true, "names": []string{"router.example.com"}}
	if w := call(in); w.Code != 400 {
		t.Fatal("missing EAB accepted")
	}
	in["eabKeyId"] = "eab-id"
	in["eabHmac"] = "eab-secret"
	if w := call(in); w.Code != 200 || strings.Contains(w.Body.String(), "dns-secret") || strings.Contains(w.Body.String(), "eab-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	if m.state.Names[0] != "router.example.com" || caDirectory(m.state) != "https://acme.zerossl.com/v2/DV90" {
		t.Fatal("options not saved")
	}
	delete(in, "accessKey")
	in["provider"] = "dnspod"
	if w := call(in); w.Code != 400 {
		t.Fatal("credentials reused across providers")
	}
	if m.state.Provider != "cloudflare" || m.state.AccessKey != "dns-secret" {
		t.Fatal("failed configuration changed credentials")
	}
}
func TestPathImportPreservesOldCertificateOnError(t *testing.T) {
	dir := t.TempDir()
	c, k := testCertificate(t, "router.example.com", time.Now().Add(time.Hour))
	cp, kp := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cp, []byte(c), 0600)
	os.WriteFile(kp, []byte(k), 0600)
	got, key, e := readCertificateFiles(cp, kp)
	if e != nil || got != c || key != k {
		t.Fatal(e)
	}
	if _, _, e = readCertificateFiles("relative.pem", kp); e == nil {
		t.Fatal("relative path accepted")
	}
	store, _ := config.Open(filepath.Join(dir, "state.json"))
	store.SetDomainSettings(config.DomainSettings{BaseDomain: "example.com"})
	m, _ := newCertificateManager(filepath.Join(dir, "cert.json"))
	a := &app{store: store, certs: m}
	call := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"action": "path", "certificatePath": cp, "privateKeyPath": kp})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(b)))
		r.RemoteAddr = "127.0.0.1:12"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	if w := call(); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	old := m.pair
	os.WriteFile(cp, []byte("bad"), 0600)
	if w := call(); w.Code != 400 || m.pair != old {
		t.Fatal("bad import replaced active certificate")
	}
}

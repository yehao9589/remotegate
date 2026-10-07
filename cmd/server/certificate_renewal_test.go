package main

import (
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func TestRenewalPolicyScheduling(t *testing.T) {
	now := time.Now()
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "cert.json"))
	c, k := testCertificate(t, "example.com", now.Add(20*24*time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	m.state = certificateState{Domain: "example.com", Source: "acme", AutoRenew: true}
	if !m.renewDue("example.com", now) {
		t.Fatal("legacy 30-day default changed")
	}
	m.state.RenewBeforeDays = 10
	if m.renewDue("example.com", now) {
		t.Fatal("renewed before configured window")
	}
	m.state.RenewBeforeDays = 25
	m.state.RetryHours = 6
	m.state.LastAttempt = now.Add(-5 * time.Hour)
	if m.renewDue("example.com", now) {
		t.Fatal("ignored retry backoff")
	}
	if !m.renewDue("example.com", now.Add(2*time.Hour)) {
		t.Fatal("did not retry after backoff")
	}
	m.state.PendingACME = true
	if m.renewDue("example.com", now.Add(2*time.Hour)) {
		t.Fatal("applied unrequested draft")
	}
	m.state.PendingACME = false
	due := m.status("example.com")["renewAfter"].(time.Time)
	if !due.Equal(m.state.LastAttempt.Add(6 * time.Hour)) {
		t.Fatal("status disagrees with scheduler", due)
	}
	// A one-week certificate must not be issued again immediately because the
	// default lead window is longer than its entire lifetime.
	leaf := &x509.Certificate{NotBefore: now, NotAfter: now.Add(7 * 24 * time.Hour)}
	if got := renewalTime(certificateState{}, leaf); !got.Equal(now.Add(112 * time.Hour)) {
		t.Fatal("short-lived certificate scheduling", got)
	}
}

func TestRenewalSettingsPersistenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	store, _ := config.Open(filepath.Join(dir, "state.json"))
	store.SetDomainSettings(config.DomainSettings{BaseDomain: "example.com"})
	m, _ := newCertificateManager(filepath.Join(dir, "cert.json"))
	a := &app{store: store, certs: m}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(data)))
		r.RemoteAddr = "127.0.0.1:12"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	in := map[string]any{"action": "configure", "email": "test@example.com", "provider": "cloudflare", "accessKey": "test-secret", "termsAccepted": true, "autoRenew": true, "renewBeforeDays": 14, "retryHours": 6}
	if w := call(in); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !m.state.PendingACME || m.state.RenewBeforeDays != 14 || m.state.RetryHours != 6 {
		t.Fatal("settings not saved")
	}
	reopened, err := newCertificateManager(m.path)
	if err != nil || reopened.state.RenewBeforeDays != 14 || reopened.state.RetryHours != 6 || !reopened.state.PendingACME {
		t.Fatal("settings not persisted", err)
	}
	for _, tc := range []struct {
		field string
		value int
	}{{"renewBeforeDays", 0}, {"renewBeforeDays", 91}, {"retryHours", 0}, {"retryHours", 169}} {
		old := in[tc.field]
		in[tc.field] = tc.value
		if w := call(in); w.Code != 400 {
			t.Fatalf("accepted invalid %s %d", tc.field, tc.value)
		}
		in[tc.field] = old
	}
	in["names"] = []string{}
	if w := call(in); w.Code != 400 {
		t.Fatal("empty explicit coverage accepted")
	}
	if m.state.RenewBeforeDays != 14 || m.state.RetryHours != 6 {
		t.Fatal("invalid request changed policy")
	}
	c, k := testCertificate(t, "example.com", time.Now().Add(20*24*time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	m.state.Source = "acme"
	m.state.PendingACME = false
	if w := call(map[string]any{"action": "renewal", "autoRenew": false}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if m.state.AutoRenew || m.state.RenewBeforeDays != 14 || m.state.RetryHours != 6 {
		t.Fatal("toggle lost configured intervals")
	}
	if w := call(map[string]any{"action": "renewal", "autoRenew": true, "renewBeforeDays": 7, "retryHours": 2}); w.Code != 200 || strings.Contains(w.Body.String(), "test-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	if m.state.RenewBeforeDays != 7 || m.state.RetryHours != 2 {
		t.Fatal("renewal form not applied")
	}
	delete(in, "names")
	if w := call(in); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if m.state.PendingACME {
		t.Fatal("unchanged application settings paused renewal")
	}
	in["names"] = []string{"example.com"}
	if w := call(in); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !m.state.PendingACME {
		t.Fatal("changed coverage applied without request")
	}
}

func TestPendingACMEReplacementKeepsImportedCertificateUntilSuccess(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "cert.json"))
	c, k := testCertificate(t, "example.com", time.Now().Add(20*24*time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	old := m.pair
	m.state = certificateState{Domain: "example.com", Provider: "cloudflare", Email: "test@example.com", AccessKey: "test-token", TermsAccepted: true, AutoRenew: true, Source: "upload", PendingACME: true, CertPEM: c, KeyPEM: k}
	newCert, newKey := testCertificate(t, "example.com", time.Now().Add(90*24*time.Hour))
	release := make(chan struct{})
	m.obtain = func(certificateState) (string, string, error) { <-release; return newCert, newKey, nil }
	if err := m.start("example.com"); err != nil {
		close(release)
		t.Fatal(err)
	}
	m.mu.Lock()
	kept := m.pair == old && m.state.Source == "upload"
	m.mu.Unlock()
	close(release)
	if !kept {
		t.Fatal("replaced imported certificate before validation")
	}
	for i := 0; i < 100; i++ {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running || m.state.Source != "acme" || m.state.PendingACME || m.state.CertPEM != newCert {
		t.Fatal("successful replacement did not apply ACME configuration")
	}
}

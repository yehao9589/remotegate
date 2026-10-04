package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func testCertificate(t *testing.T, domain string, expires time.Time) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "RemoteGate test certificate"}, DNSNames: []string{"*." + domain, domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
}
func TestCertificateValidation(t *testing.T) {
	cert, key := testCertificate(t, "example.com", time.Now().Add(90*24*time.Hour))
	_, other := testCertificate(t, "example.com", time.Now().Add(time.Hour))
	for _, tc := range []struct {
		cert, key, domain string
		valid             bool
	}{{cert, key, "example.com", true}, {cert, other, "example.com", false}, {cert, key, "other.com", false}, {"bad", key, "example.com", false}} {
		_, _, err := validateCertificate(tc.cert, tc.key, tc.domain)
		if (err == nil) != tc.valid {
			t.Fatalf("unexpected validation: %v", err)
		}
	}
	expired, k := testCertificate(t, "example.com", time.Now().Add(-time.Minute))
	if _, _, err := validateCertificate(expired, k, "example.com"); err == nil {
		t.Fatal("expired accepted")
	}
}
func TestCertificateUploadAndSecrets(t *testing.T) {
	dir := t.TempDir()
	m, _ := newCertificateManager(filepath.Join(dir, "managed.json"))
	store, _ := config.Open(filepath.Join(dir, "state.json"))
	store.SetDomainSettings(config.DomainSettings{BaseDomain: "example.com"})
	a := &app{store: store, certs: m}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/admin/certificates", strings.NewReader(string(raw)))
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		a.certificates(w, r)
		return w
	}
	cert, key := testCertificate(t, "example.com", time.Now().Add(90*24*time.Hour))
	w := call(map[string]any{"action": "upload", "certificate": cert, "privateKey": key})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("private key leaked")
	}
	w = call(map[string]any{"action": "configure", "email": "test@example.com", "accessKey": "secret-id", "secretKey": "secret-value", "termsAccepted": true, "autoRenew": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-value") || strings.Contains(w.Body.String(), "secret-id") {
		t.Fatal("DNS credentials leaked")
	}
	old := m.pair
	w = call(map[string]any{"action": "upload", "certificate": "bad", "privateKey": key})
	if w.Code == 200 || m.pair != old {
		t.Fatal("bad upload changed certificate")
	}
	reopened, err := newCertificateManager(m.path)
	if err != nil || reopened.pair == nil {
		t.Fatal("certificate not persisted", err)
	}
	if m.renewDue("example.com", time.Now()) {
		t.Fatal("manual certificate auto renewed")
	}
}
func TestTLSCertificateHotReload(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "managed.json"))
	c, k := testCertificate(t, "example.com", time.Now().Add(time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	ts.TLS = &tls.Config{GetCertificate: m.getCertificate}
	ts.StartTLS()
	defer ts.Close()
	// Trust the local test certificate explicitly; validation stays enabled.
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(c))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "console.example.com"}}}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c2, k2 := testCertificate(t, "example.com", time.Now().Add(2*time.Hour))
	pair, _, _ := validateCertificate(c2, k2, "example.com")
	m.mu.Lock()
	m.pair = pair
	m.mu.Unlock()
	roots2 := x509.NewCertPool()
	roots2.AppendCertsFromPEM([]byte(c2))
	client2 := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots2, ServerName: "console.example.com"}}}
	resp, err = client2.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.TLS.PeerCertificates[0].SerialNumber.Cmp(pair.Leaf.SerialNumber) != 0 {
		t.Fatal("old certificate still served")
	}
}
func TestRenewalJob(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "managed.json"))
	c, k := testCertificate(t, "example.com", time.Now().Add(20*24*time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	m.state = certificateState{Domain: "example.com", Email: "test@example.com", AccessKey: "id", SecretKey: "secret", TermsAccepted: true, AutoRenew: true, Source: "acme", CertPEM: c, KeyPEM: k}
	if !m.renewDue("example.com", time.Now()) || m.renewDue("other.com", time.Now()) {
		t.Fatal("renewal scheduling incorrect")
	}
	newCert, newKey := testCertificate(t, "example.com", time.Now().Add(90*24*time.Hour))
	m.obtain = func(certificateState) (string, string, error) { return newCert, newKey, nil }
	if err := m.start("example.com"); err != nil {
		t.Fatal(err)
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
	if m.running || m.state.CertPEM != newCert {
		t.Fatal("renewal not applied")
	}
}

func TestDNSCleanupOnlyOwnRecord(t *testing.T) {
	t.Setenv("LEGO_DISABLE_CNAME_SUPPORT", "true")
	var deleted []string
	p := &aliDNSChallenge{records: map[string]string{}, zone: func(string) (string, error) { return "example.com.", nil }, add: func(string, string, string) (string, error) { return "created-id", nil }, remove: func(id string) error { deleted = append(deleted, id); return nil }}
	if err := p.CleanUp("example.com", "unknown", "auth"); err != nil || len(deleted) != 0 {
		t.Fatal("unowned record deleted")
	}
	if err := p.Present("example.com", "token", "auth"); err != nil {
		t.Fatal(err)
	}
	if err := p.CleanUp("example.com", "token", "auth"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "created-id" {
		t.Fatal("incorrect cleanup")
	}
}

func TestRenewalFailureKeepsCertificate(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "managed.json"))
	c, k := testCertificate(t, "example.com", time.Now().Add(20*24*time.Hour))
	m.pair, _, _ = validateCertificate(c, k, "example.com")
	old := m.pair
	m.state = certificateState{Domain: "example.com", Email: "test@example.com", AccessKey: "test-id", SecretKey: "test-secret", TermsAccepted: true, AutoRenew: true, Source: "acme", CertPEM: c, KeyPEM: k}
	m.obtain = func(certificateState) (string, string, error) {
		return "", "", fmt.Errorf("provider rejected test-id test-secret")
	}
	if err := m.start("example.com"); err != nil {
		t.Fatal(err)
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
	if m.pair != old || m.state.CertPEM != c {
		t.Fatal("failed renewal replaced certificate")
	}
	if strings.Contains(m.state.LastResult, "test-secret") || strings.Contains(m.state.LastResult, "test-id") {
		t.Fatal("secret in error")
	}
	m.mu.Unlock()
	if m.renewDue("example.com", time.Now()) {
		t.Fatal("renewed before backoff elapsed")
	}
}
func TestCertificateRemoteHTTPRejected(t *testing.T) {
	m, _ := newCertificateManager(filepath.Join(t.TempDir(), "managed.json"))
	store, _ := config.Open(filepath.Join(t.TempDir(), "state.json"))
	a := &app{store: store, certs: m}
	r := httptest.NewRequest("POST", "/api/admin/certificates", strings.NewReader(`{"action":"configure"}`))
	r.RemoteAddr = "203.0.113.5:9999"
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	a.certificates(w, r)
	if w.Code != 400 {
		t.Fatal("accepted untrusted HTTP credentials")
	}
}

func TestCertificateManagersAreIsolatedByDomain(t *testing.T) {
	a := &app{certDir: t.TempDir(), certManagers: map[string]*certificateManager{}}
	one, err := a.certificateFor("one.example")
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.certificateFor("two.example")
	if err != nil {
		t.Fatal(err)
	}
	if one == two || one.path == two.path {
		t.Fatal("domains share certificate storage")
	}
	if again, _ := a.certificateFor("one.example"); again != one {
		t.Fatal("manager was not reused")
	}
}

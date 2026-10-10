package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func TestPublicCertificateInspectionVerifiesTrustHostAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, host         string
		expired, untrusted bool
	}{
		{name: "trusted", host: "router.example.com"},
		{name: "wrong-domain", host: "other.example.test"},
		{name: "untrusted", host: "router.example.com", untrusted: true},
		{name: "expired", host: "router.example.com", expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := time.Now().Add(24 * time.Hour)
			if tc.expired {
				expires = time.Now().Add(-time.Hour)
			}
			cert, key := testCertificate(t, "example.com", expires)
			pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			roots := x509.NewCertPool()
			if !tc.untrusted {
				roots.AppendCertsFromPEM([]byte(cert))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := inspectHTTPSCertificate(ctx, strings.TrimPrefix(server.URL, "https://"), tc.host, roots)
			if err != nil {
				t.Fatal(err)
			}
			wantValid := tc.name == "trusted"
			if result["valid"] != wantValid || requests.Load() != 0 {
				t.Fatal("incorrect verification or HTTP request sent", result)
			}
			block, _ := pem.Decode([]byte(cert))
			leaf, _ := x509.ParseCertificate(block.Bytes)
			if result["issuer"] != leaf.Issuer.CommonName || result["expiresAt"] != leaf.NotAfter || len(result["names"].([]string)) != 2 {
				t.Fatal("actual peer metadata missing", result)
			}
			if !wantValid && result["error"] == nil {
				t.Fatal("failed verification presented without explanation")
			}
		})
	}
}

func TestDetectionOnlyDoesNotRenewUsingSavedDNSCredentials(t *testing.T) {
	for _, tc := range []struct{ provider, address string }{{"external", ":443"}, {"remotegate", ""}} {
		t.Run(tc.provider+tc.address, func(t *testing.T) {
			store, _ := config.Open(filepath.Join(t.TempDir(), "state.json"))
			d, _ := store.PutDomain(config.DomainSettings{BaseDomain: "example.com", RootHTTPSProvider: tc.provider})
			m, _ := newCertificateManager(filepath.Join(t.TempDir(), "cert.json"))
			cert, key := testCertificate(t, d.BaseDomain, time.Now().Add(10*24*time.Hour))
			m.pair, _, _ = validateCertificate(cert, key, d.BaseDomain)
			m.state = certificateState{Domain: d.BaseDomain, Source: "acme", AutoRenew: true, TermsAccepted: true, Provider: "cloudflare", AccessKey: "saved-dns-secret", Email: "test@example.com"}
			if !m.renewDue(d.BaseDomain, time.Now()) {
				t.Fatal("fixture not due for renewal")
			}
			a := &app{store: store, certs: m, httpsAddress: tc.address}
			a.renewDomainCertificates(time.Now())
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.running || !m.state.LastAttempt.IsZero() {
				t.Fatal("read-only certificate mode started renewal")
			}
		})
	}
}

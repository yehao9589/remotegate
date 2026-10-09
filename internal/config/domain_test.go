package config

import (
	"testing"
	"time"
)

func TestExternalHTTPSObservationPersistenceAndScope(t *testing.T) {
	path := t.TempDir() + "/state.json"
	s, _ := Open(path)
	d, err := s.PutDomain(DomainSettings{BaseDomain: "gate.example.com", RootHTTPSProvider: "external"})
	if err != nil {
		t.Fatal(err)
	}
	check := PublicHTTPSCheck{Host: d.BaseDomain, Port: 443, CheckedAt: time.Now(), Valid: true, Names: []string{d.BaseDomain}, ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := s.SetDomainHTTPSCheck(d.ID, check); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.Domains()[0].PublicHTTPS == nil || !reopened.Domains()[0].PublicHTTPS.Valid || reopened.Domains()[0].RootHTTPSProvider != "external" {
		t.Fatal("external HTTPS state not retained", err)
	}
	read := s.Domains()[0]
	read.PublicHTTPS.Names[0] = "forged.example.com"
	if s.Domains()[0].PublicHTTPS.Names[0] != d.BaseDomain {
		t.Fatal("returned observation mutated store")
	}
	check.Host = "router." + d.BaseDomain
	if s.SetDomainHTTPSCheck(d.ID, check) == nil {
		t.Fatal("child verification accepted as root verification")
	}
	check.Host = d.BaseDomain
	check.Port = 8443
	if s.SetDomainHTTPSCheck(d.ID, check) == nil {
		t.Fatal("another port accepted as root verification")
	}
	d.PublicHTTPS = &PublicHTTPSCheck{Host: d.BaseDomain, Valid: false}
	d.Name = "renamed"
	d, err = s.PutDomain(d)
	if err != nil || d.PublicHTTPS == nil || !d.PublicHTTPS.Valid {
		t.Fatal("settings overwrote server observation", err)
	}
	d.RootHTTPSPort = 8443
	d, err = s.PutDomain(d)
	if err != nil || d.PublicHTTPS != nil {
		t.Fatal("port change kept stale observation", err)
	}
	if err = s.SetDomainHTTPSCheck(d.ID, check); err != nil {
		t.Fatal(err)
	}
	d.BaseDomain = "new.example.com"
	d, err = s.PutDomain(d)
	if err != nil || d.PublicHTTPS != nil {
		t.Fatal("domain change kept old result", err)
	}
}

func TestPublicHTTPSObservationCannotBeProvidedInDomainSettings(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	d, err := s.PutDomain(DomainSettings{BaseDomain: "example.com", PublicHTTPS: &PublicHTTPSCheck{Valid: true}})
	if err != nil || d.PublicHTTPS != nil {
		t.Fatal("client supplied verification accepted", err)
	}
	for _, d := range []DomainSettings{{BaseDomain: "example.com", RootHTTPSProvider: "bad"}, {BaseDomain: "example.com", RootHTTPSPort: -1}, {BaseDomain: "example.com", RootHTTPSPort: 65536}} {
		if _, err := s.PutDomain(d); err == nil {
			t.Fatal("invalid HTTPS settings accepted", d)
		}
	}
}

func TestDomainSettingsPersistence(t *testing.T) {
	p := t.TempDir() + "/state.json"
	s, _ := Open(p)
	if err := s.SetDomainSettings(DomainSettings{BaseDomain: " Example.COM ", ServerIP: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"https://example.com", "*.example.com", "example.com:443", "127.0.0.1", "bad..xyz"} {
		if s.SetDomainSettings(DomainSettings{BaseDomain: v}) == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	reopened, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DomainSettings().BaseDomain != "example.com" {
		t.Fatal("settings not persisted")
	}
	if _, _, err = s.CreateDevice("keep-domain"); err != nil {
		t.Fatal(err)
	}
	reopened, _ = Open(p)
	if reopened.DomainSettings().BaseDomain != "example.com" {
		t.Fatal("device write lost domain")
	}
}

func TestMultipleDomainsAndDeleteGuard(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	a, err := s.PutDomain(DomainSettings{Name: "阿里云", BaseDomain: "one.example", ServerIP: "203.0.113.1", DNSProvider: "alidns"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PutDomain(DomainSettings{Name: "腾讯云", BaseDomain: "two.example", DNSProvider: "dnspod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Domains()) != 2 || a.ID == b.ID {
		t.Fatal("multiple domains not retained")
	}
	dev, _, _ := s.CreateDevice("router")
	s.PutMapping(Mapping{Host: "home.one.example", DeviceID: dev.ID, Target: "http://127.0.0.1", Enabled: true})
	if err = s.DeleteDomain(a.ID); err == nil {
		t.Fatal("deleted domain with mappings")
	}
	if err = s.DeleteDomain(b.ID); err != nil {
		t.Fatal(err)
	}
}

package config

import "testing"

func TestDomainSettingsPersistence(t *testing.T) {
	p := t.TempDir() + "/state.json"
	s, _ := Open(p)
	if err := s.SetDomainSettings(DomainSettings{BaseDomain: " Fanke.XYZ ", ServerIP: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"https://fanke.xyz", "*.fanke.xyz", "fanke.xyz:443", "127.0.0.1", "bad..xyz"} {
		if s.SetDomainSettings(DomainSettings{BaseDomain: v}) == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	reopened, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DomainSettings().BaseDomain != "fanke.xyz" {
		t.Fatal("settings not persisted")
	}
	if _, _, err = s.CreateDevice("keep-domain"); err != nil {
		t.Fatal(err)
	}
	reopened, _ = Open(p)
	if reopened.DomainSettings().BaseDomain != "fanke.xyz" {
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

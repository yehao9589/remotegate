package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAdminDeviceAndMappingLifecycle(t *testing.T) {
	s, err := Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	d, token, err := s.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.PutMapping(Mapping{Host: "one.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PutMapping(Mapping{Host: "two.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:81", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	first.Host = "two.example.com"
	if _, err = s.PutMapping(first); err == nil {
		t.Fatal("duplicate edit accepted")
	}
	if err = s.ChangeDevice(d.ID, "renamed", false); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Authenticate(d.ID, token); !ok || got.Name != "renamed" {
		t.Fatal("rename broke credentials")
	}
	if err = s.ChangeDevice(d.ID, "", true); err == nil {
		t.Fatal("deleted device with mappings")
	}
	for _, m := range s.Snapshot().Mappings {
		if err = s.DeleteMapping(m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ChangeDevice(d.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(d.ID, token); ok {
		t.Fatal("deleted credentials still accepted")
	}
}

func TestMappingHostNormalizationAndUniqueness(t *testing.T) {
	s, err := Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := s.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMapping(Mapping{Host: "Router.Example.COM:443", DeviceID: d.ID, Target: "http://127.0.0.1:80/", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.Host != "router.example.com" || m.Target != "http://127.0.0.1:80" {
		t.Fatalf("unexpected normalization: %#v", m)
	}
	if m.PublicScheme != "https" || m.PublicPort != 443 {
		t.Fatalf("unexpected public endpoint: %#v", m)
	}
	if _, ok := s.MappingForHost("ROUTER.EXAMPLE.COM:443"); !ok {
		t.Fatal("mapping was not found")
	}
	if _, err = s.PutMapping(Mapping{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:81", Enabled: true}); err == nil {
		t.Fatal("duplicate host was accepted")
	}
}

func TestMappingValidationAndLocalDefaults(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	d, _, _ := s.CreateDevice("router")
	m, err := s.PutMapping(Mapping{Host: "router.localhost", DeviceID: d.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil || m.PublicScheme != "http" || m.PublicPort != 80 {
		t.Fatal(m, err)
	}
	for _, bad := range []Mapping{
		{Host: "*.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80"},
		{Host: "router.example.com", DeviceID: d.ID, Target: "ftp://127.0.0.1:21"},
		{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:70000"},
		{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80", PublicScheme: "ftp", PublicPort: 21},
	} {
		if _, err = s.PutMapping(bad); err == nil {
			t.Fatalf("accepted invalid mapping: %#v", bad)
		}
	}
}

func TestOpenMigratesLegacyMappingPublicEndpoint(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "127.0.0.1:18088")
	path := t.TempDir() + "/state.json"
	legacy := State{Mappings: []Mapping{
		{Host: "router.localhost"},
		{Host: "router.example.com"},
	}}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot().Mappings
	byHost := map[string]Mapping{got[0].Host: got[0], got[1].Host: got[1]}
	if byHost["router.localhost"].PublicScheme != "http" || byHost["router.localhost"].PublicPort != 18088 || byHost["router.example.com"].PublicScheme != "https" || byHost["router.example.com"].PublicPort != 443 {
		t.Fatalf("legacy mappings were not migrated: %#v", got)
	}
	reopened, err := Open(path)
	if err != nil || reopened.Snapshot().Mappings[0].PublicPort == 0 || reopened.Snapshot().Mappings[1].PublicPort == 0 {
		t.Fatal("migration was not persisted", err)
	}
}

func TestTokenIsStoredAsHash(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	d, token, err := s.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	if d.TokenHash == token || d.TokenHash == "" {
		t.Fatal("token was not hashed")
	}
	if _, ok := s.Authenticate(d.ID, token); !ok {
		t.Fatal("valid token rejected")
	}
	if _, ok := s.Authenticate(d.ID, "wrong"); ok {
		t.Fatal("invalid token accepted")
	}
}

func TestEmptySnapshotUsesJSONArrays(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	state := s.Snapshot()
	if state.Devices == nil || state.Mappings == nil {
		t.Fatal("empty collections must be JSON arrays, not null")
	}
}

func TestPendingEnrollment(t *testing.T) {
	path := t.TempDir() + "/state.json"
	s, _ := Open(path)
	d, token, err := s.CreatePendingDevice("pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Devices) != 0 {
		t.Fatal("pending device listed")
	}
	reopened, _ := Open(path)
	if _, ok := reopened.Authenticate(d.ID, token); ok {
		t.Fatal("pending persisted")
	}
	if err = s.Activate(d.ID, "bad", "0.1.0"); err == nil {
		t.Fatal("invalid token accepted")
	}
	if err = s.Activate(d.ID, token, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	reopened, _ = Open(path)
	got, ok := reopened.Authenticate(d.ID, token)
	if !ok || got.Version != "0.1.0" {
		t.Fatal("activation/version not persisted")
	}
	if err = s.Activate(d.ID, token, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Devices) != 1 {
		t.Fatal("duplicate activation")
	}
}

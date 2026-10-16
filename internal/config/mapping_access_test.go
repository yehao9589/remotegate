package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMappingAccessDefaultsAndPersistedLegacyMigration(t *testing.T) {
	path := t.TempDir() + "/state.json"
	legacy := State{Mappings: []Mapping{
		{ID: "legacy", Host: "old.example.com", Target: "http://127.0.0.1:80", DeviceID: "router", Enabled: true},
		{ID: "public", Host: "public.example.com", AccessMode: "public"},
	}}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindMappingAccessConsole("https://console.example.com"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Snapshot().Mappings {
		if m.ID == "legacy" && (m.AccessMode != "admin" || m.AccessConsoleURL != "https://console.example.com" || m.Target != "http://127.0.0.1:80" || !m.Enabled) {
			t.Fatal(m)
		}
		if m.ID == "public" && (m.AccessMode != "public" || m.AccessConsoleURL != "") {
			t.Fatal(m)
		}
	}
	d, _, _ := s.CreateDevice("new")
	m, err := s.PutMapping(Mapping{Host: "new.example.com", Target: "http://127.0.0.1:80", DeviceID: d.ID})
	if err != nil || m.AccessMode != "admin" || m.AccessRevision == "" {
		t.Fatal(m, err)
	}
	previous := m.AccessRevision
	m.Note = "only a note"
	m, err = s.PutMapping(m)
	if err != nil || m.AccessRevision != previous {
		t.Fatal("Note change revoked session", err)
	}
	m.AccessRevision = "caller-controlled"
	m.Target = "http://127.0.0.1:8080"
	m, err = s.PutMapping(m)
	if err != nil || m.AccessRevision == previous || m.AccessRevision == "caller-controlled" {
		t.Fatal("Security change must revoke sessions", m, err)
	}
	if err = s.BindMappingAccessConsole("https://different.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Snapshot().Mappings {
		if m.ID == "legacy" && m.AccessConsoleURL != "https://console.example.com" {
			t.Fatal("Existing binding overwritten")
		}
	}
}

func TestMappingAccessRejectsUnsafeConfiguration(t *testing.T) {
	s, _ := Open(t.TempDir() + "/state.json")
	d, _, _ := s.CreateDevice("router")
	base := Mapping{Host: "router.example.com", Target: "http://127.0.0.1:80", DeviceID: d.ID}
	for _, mode := range []string{"anonymous", "ADMIN"} {
		m := base
		m.AccessMode = mode
		if _, err := s.PutMapping(m); err == nil {
			t.Fatal("Accepted invalid mode", mode)
		}
	}
	m := base
	m.PublicScheme = "http"
	if _, err := s.PutMapping(m); err == nil {
		t.Fatal("Protected public HTTP mapping accepted")
	}
	m.AccessMode = "public"
	if _, err := s.PutMapping(m); err != nil {
		t.Fatal("Explicit public HTTP mapping rejected", err)
	}
	for _, address := range []string{"http://console.example.com", "https://user:pass@example.com", "https://console.example.com/path", "https://console.example.com/?next=evil", "https://console.example.com:65536", "https://console.example.com:0", "https://console..example.com", "https://router.example.com"} {
		m = base
		m.AccessConsoleURL = address
		if _, err := s.PutMapping(m); err == nil {
			t.Fatal("Unsafe console accepted", address)
		}
	}
	for _, address := range []string{"https://console.example.com", "http://localhost:18088", "http://console.localhost:18088", "http://127.0.0.1:18088", "http://[::1]:18088"} {
		if err := ValidateAccessConsole(address); err != nil {
			t.Fatal(address, err)
		}
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevicePreferencesAndConnectionHistoryPersistWithoutChangingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, token, err := s.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMapping(Mapping{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	name, note, pinned := "主路由器", "家里", true
	if err := s.UpdateDevice(d.ID, &name, &note, &pinned); err != nil {
		t.Fatal(err)
	}
	rename := "新名称"
	if err := s.UpdateDevice(d.ID, &rename, nil, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	info := DeviceInfo{OS: "linux", Arch: "arm64", Hostname: "OpenWrt", Interface: "pppoe-wan", PeerIP: "203.0.113.8"}
	if err := s.RecordConnection(d.ID, at, info); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDisconnect(d.ID, at, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	next := at.Add(2 * time.Minute)
	if err := s.RecordConnection(d.ID, next, info); err != nil {
		t.Fatal(err)
	}
	// The old read loop may exit after its replacement was attached.
	if err := s.RecordDisconnect(d.ID, at, next.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot().Devices[0]
	if got.Name != rename || got.Note != note || !got.Pinned || got.Info != info || !got.LastConnectedAt.Equal(next) || !got.LastDisconnectedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("lost history or preferences: %+v", got)
	}
	if _, ok := restored.Authenticate(d.ID, token); !ok {
		t.Fatal("credentials changed")
	}
	if got, ok := restored.MappingForHost(m.Host); !ok || got.ID != m.ID {
		t.Fatal("mapping changed")
	}
	if err := restored.RecordConnection(d.ID, next.Add(time.Minute), DeviceInfo{PeerIP: "203.0.113.8"}); err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().Devices[0].Info.OS != "" {
		t.Fatal("legacy connection reused stale system metadata")
	}
}

func TestDeviceEditValidationAndWriteFailureAreAtomic(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	d, _, _ := s.CreateDevice("original")
	name, note, pin := "new name", strings.Repeat("中", 201), true
	if err := s.UpdateDevice(d.ID, &name, &note, &pin); err == nil {
		t.Fatal("long note accepted")
	}
	if s.Snapshot().Devices[0] != d {
		t.Fatal("invalid edit changed device")
	}
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocker, "state.json")
	note = "valid note"
	if err := s.UpdateDevice(d.ID, &name, &note, &pin); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if s.Snapshot().Devices[0] != d {
		t.Fatal("failed write changed device")
	}
	if err := s.RecordConnection(d.ID, time.Now(), DeviceInfo{OS: "linux"}); err == nil {
		t.Fatal("history write unexpectedly succeeded")
	}
	if s.Snapshot().Devices[0] != d {
		t.Fatal("failed history write changed device")
	}
}

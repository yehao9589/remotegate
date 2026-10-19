package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEgressReportPersistsWithoutLifecycleEventsAndRejectsStaleSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, _, _ := s.CreateDevice("router")
	at := time.Now().UTC()
	info := DeviceInfo{OS: "linux", PeerIP: "203.0.113.1"}
	if err := s.RecordConnection(d.ID, at, info); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEgressInterface(d.ID, at, "pppoe-wan"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot().Devices[0].Info
	if got.EgressInterface != "pppoe-wan" || got.Interface != "" || got.PeerIP != info.PeerIP {
		t.Fatal("lost actual/preferred interface distinction", got)
	}
	events, _ := s.DeviceConnectionEvents(d.ID)
	if len(events) != 1 {
		t.Fatal("metadata created a lifecycle event")
	}
	for _, bad := range []string{"wan\nspoof", strings.Repeat("x", 129), "wan\x00bad"} {
		if s.RecordEgressInterface(d.ID, at, bad) == nil {
			t.Fatal("invalid interface accepted")
		}
	}
	next := at.Add(time.Second)
	s.RecordConnection(d.ID, next, info)
	s.RecordEgressInterface(d.ID, at, "stale")
	if s.Snapshot().Devices[0].Info.EgressInterface != "" {
		t.Fatal("old session leaked into new connection")
	}
	s.RecordEgressInterface(d.ID, next, "eth1")
	s.RecordDisconnect(d.ID, next, next.Add(time.Second))
	s.RecordEgressInterface(d.ID, next, "closed")
	if s.Snapshot().Devices[0].Info.EgressInterface != "eth1" {
		t.Fatal("closed session changed last interface")
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionEventsPersistBoundedAndIsolated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, _, _ := s.CreateDevice("router")
	other, _, _ := s.CreateDevice("other")
	at := time.Now().UTC()
	for i := 0; i < 65; i++ {
		start := at.Add(time.Duration(i) * time.Minute)
		if err := s.RecordConnection(d.ID, start, DeviceInfo{}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordConnection(d.ID, start, DeviceInfo{}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordDisconnect(d.ID, start, start.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordDisconnect(d.ID, start, start.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, ok := s.DeviceConnectionEvents(d.ID)
	if !ok || len(events) != ConnectionEventLimit || events[0].State != "offline" || !events[0].At.Equal(at.Add(64*time.Minute+time.Second)) || !events[99].At.Equal(at.Add(15*time.Minute)) {
		t.Fatal("wrong retention/order/deduplication", events)
	}
	events[0].Reason = "mutated"
	snapshot := s.Snapshot()
	snapshot.ConnectionEvents[d.ID][0].Reason = "mutated"
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*Store{s, restored} {
		got, _ := store.DeviceConnectionEvents(d.ID)
		if got[0].Reason != "disconnected" || got[99].Reason != "connected" {
			t.Fatal("history alias or persistence failure")
		}
		otherEvents, ok := store.DeviceConnectionEvents(other.ID)
		if !ok || len(otherEvents) != 0 {
			t.Fatal("history crossed device boundary")
		}
		if _, ok := store.DeviceConnectionEvents("missing"); ok {
			t.Fatal("missing device accepted")
		}
	}
	if err := restored.ChangeDevice(d.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot().ConnectionEvents) != 0 {
		t.Fatal("deleted device retained history")
	}
}

func TestConnectionEventsRejectOldSessionsAndRecoverAfterCrash(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	d, _, _ := s.CreateDevice("router")
	at := time.Now().UTC()
	s.RecordConnection(d.ID, at, DeviceInfo{})
	next := at.Add(time.Minute)
	s.RecordConnection(d.ID, next, DeviceInfo{})
	s.RecordConnection(d.ID, at, DeviceInfo{OS: "stale"})
	s.RecordDisconnect(d.ID, at, next.Add(time.Second))
	s.RecordDisconnect(d.ID, next, at) // Invalid negative duration.
	got, _ := s.DeviceConnectionEvents(d.ID)
	if len(got) != 2 || got[0].Reason != "replaced" || s.Snapshot().Devices[0].Info.OS != "" {
		t.Fatal("stale/invalid event accepted", got)
	}
	if err := s.RecoverConnections(next.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverConnections(next.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.DeviceConnectionEvents(d.ID)
	if len(got) != 3 || got[0].Reason != "server_restart" || !got[0].ConnectedAt.Equal(next) {
		t.Fatal("recovery missing or duplicated", got)
	}
}

func TestConnectionEventWriteFailureRollsBackAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, _, _ := s.CreateDevice("router")
	at := time.Now().UTC()
	s.RecordConnection(d.ID, at, DeviceInfo{})
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocker, "state.json")
	if err := s.RecordDisconnect(d.ID, at, at.Add(time.Second)); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	got, _ := s.DeviceConnectionEvents(d.ID)
	if len(got) != 1 || !s.Snapshot().Devices[0].LastDisconnectedAt.IsZero() {
		t.Fatal("failed write mutated memory")
	}
	if err := s.ChangeDevice(d.ID, "", true); err == nil {
		t.Fatal("delete save unexpectedly succeeded")
	}
	if _, ok := s.DeviceConnectionEvents(d.ID); !ok {
		t.Fatal("failed delete lost device/history")
	}
	s.path = path
	if err := s.RecordDisconnect(d.ID, at, at.Add(time.Second), "reconnect"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.DeviceConnectionEvents(d.ID)
	if len(got) != 2 || got[0].Reason != "reconnect" {
		t.Fatal("retry did not save exact event", got)
	}
}

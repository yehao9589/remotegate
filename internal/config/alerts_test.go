package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAlertLifecycleDedupReadRecoveryRecurrenceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	now := time.Now().UTC()
	condition := []Alert{{Key: "offline:r", Kind: "offline", Title: "Router offline"}}
	if err := s.ReconcileAlerts(condition, now); err != nil {
		t.Fatal(err)
	}
	first := s.Alerts()[0]
	if first.Read || first.ResolvedAt != nil {
		t.Fatal(first)
	}
	s.ReadAlerts()
	s.ReconcileAlerts(condition, now.Add(time.Minute))
	if len(s.Alerts()) != 1 || !s.Alerts()[0].Read || s.Alerts()[0].ID != first.ID {
		t.Fatal("duplicate or unread reset")
	}
	s.ReconcileAlerts(nil, now.Add(2*time.Minute))
	resolved := s.Alerts()[0]
	if resolved.ResolvedAt == nil || resolved.Read {
		t.Fatal("recovery not recorded")
	}
	*resolved.ResolvedAt = time.Time{}
	if s.Alerts()[0].ResolvedAt.IsZero() {
		t.Fatal("snapshot aliases resolution")
	}
	s.ReconcileAlerts(condition, now.Add(3*time.Minute))
	if len(s.Alerts()) != 2 || s.Alerts()[0].ID == first.ID {
		t.Fatal("recurrence not recorded")
	}
	restored, err := Open(path)
	if err != nil || len(restored.Alerts()) != 2 {
		t.Fatal("restart lost alerts", err)
	}
}

func TestAlertsBoundedWithoutRepeatedEvictionNotifications(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	conditions := []Alert{}
	for i := 0; i < 250; i++ {
		conditions = append(conditions, Alert{Key: fmt.Sprintf("device:%03d", i)})
	}
	s.ReconcileAlerts(conditions, time.Now())
	s.ReadAlerts()
	for i := 0; i < 3; i++ {
		s.ReconcileAlerts(conditions, time.Now())
	}
	if len(s.Alerts()) != 200 {
		t.Fatal("unbounded alerts")
	}
	for _, a := range s.Alerts() {
		if !a.Read {
			t.Fatal("eviction created repeated unread alerts")
		}
	}
	s.ReconcileAlerts([]Alert{{Key: "aaa:new"}}, time.Now())
	if len(s.Alerts()) != 200 || s.Alerts()[0].Key != "aaa:new" {
		t.Fatal("new incident lost")
	}
}

func TestAlertWriteFailureDoesNotDiscardPendingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "state.json")
	s, _ := Open(path)
	os.WriteFile(filepath.Join(dir, "data"), []byte("blocked"), 0600)
	if s.ReconcileAlerts([]Alert{{Key: "test"}}, time.Now()) == nil || len(s.Alerts()) != 0 {
		t.Fatal("failed write mutated state")
	}
	os.Remove(filepath.Join(dir, "data"))
	if err := s.ReconcileAlerts([]Alert{{Key: "test"}}, time.Now()); err != nil || len(s.Alerts()) != 1 {
		t.Fatal("retry failed", err)
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceConnectionLimitsPersistAndFailedSaveRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, token, err := s.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.DeviceConnectionLimits(d.ID); got != DefaultConnectionLimits() {
		t.Fatal(got)
	}
	limits := ConnectionLimits{24, 8}
	if err := s.SetDeviceConnectionLimits(d.ID, limits); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DeviceConnectionLimits(d.ID) != limits {
		t.Fatal("limits were not persisted")
	}
	if _, valid := reopened.Authenticate(d.ID, token); !valid {
		t.Fatal("limits changed device credentials")
	}
	for _, invalid := range []ConnectionLimits{{0, 8}, {1, 3}, {125, 4}, {64, 65}, {100, 29}, {-1, 8}, {8, 0}} {
		if reopened.SetDeviceConnectionLimits(d.ID, invalid) == nil {
			t.Fatal("accepted invalid limits", invalid)
		}
		if reopened.DeviceConnectionLimits(d.ID) != limits {
			t.Fatal("invalid settings changed limits")
		}
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened.path = filepath.Join(blocked, "state.json")
	if reopened.SetDeviceConnectionLimits(d.ID, ConnectionLimits{32, 8}) == nil {
		t.Fatal("expected save failure")
	}
	if reopened.DeviceConnectionLimits(d.ID) != limits {
		t.Fatal("failed save changed live limits")
	}
	if err := s.SetDeviceConnectionLimits("missing", limits); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

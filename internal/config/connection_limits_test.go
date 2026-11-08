package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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
	limits := ConnectionLimits{24, 8, 0, 0}
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
	for _, invalid := range []ConnectionLimits{{0, 8, 0, 0}, {1, 3, 0, 0}, {125, 4, 0, 0}, {64, 65, 0, 0}, {100, 29, 0, 0}, {-1, 8, 0, 0}, {8, 0, 0, 0}} {
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
	if reopened.SetDeviceConnectionLimits(d.ID, ConnectionLimits{32, 8, 0, 0}) == nil {
		t.Fatal("expected save failure")
	}
	if reopened.DeviceConnectionLimits(d.ID) != limits {
		t.Fatal("failed save changed live limits")
	}
	if err := s.SetDeviceConnectionLimits("missing", limits); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestBrowserPausePolicyPersistsWithConnectionLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, _, _ := s.CreateDevice("router")
	limits := ConnectionLimits{WebSockets: 24, HTTPReserve: 8, IdleMinutes: 5, BackgroundMinutes: 2}
	if _, err := s.SetConnectionLimitDefaults(limits, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DeviceConnectionLimits(d.ID) != limits || reopened.ConnectionLimitDefaults().Defaults != limits {
		t.Fatal("pause policy lost during persistence")
	}
	for _, bad := range []ConnectionLimits{{WebSockets: 8, HTTPReserve: 8, IdleMinutes: 241}, {WebSockets: 8, HTTPReserve: 8, BackgroundMinutes: 61}, {WebSockets: 8, HTTPReserve: 8, IdleMinutes: -2}} {
		if reopened.SetDeviceConnectionLimits(d.ID, bad) == nil {
			t.Fatal("invalid timeout accepted", bad)
		}
	}
	idle, background := DefaultConnectionLimits().ActivityTimeouts()
	if idle != 15 || background != 1 {
		t.Fatal(idle, background)
	}
}

func TestConnectionLimitDefaultsOnlyAffectNewDevicesUntilAppliedToAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	legacy, legacyToken, _ := s.CreateDevice("legacy router")
	// Model an existing state file predating per-device connection settings.
	s.state.Devices[0].ConnectionLimits = ConnectionLimits{}
	custom, customToken, _ := s.CreateDevice("offline custom router")
	if err := s.SetDeviceConnectionLimits(custom.ID, ConnectionLimits{12, 4, 0, 0}); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.PutMapping(Mapping{Host: "router.example.test", DeviceID: legacy.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pending, pendingToken, _ := s.CreatePendingDevice("old pending")
	defaults := ConnectionLimits{24, 8, 0, 0}
	result, err := s.SetConnectionLimitDefaults(defaults, false)
	if err != nil || result.Defaults != defaults || result.DeviceCount != 2 || result.AppliedCount != 0 {
		t.Fatal(result, err)
	}
	if s.DeviceConnectionLimits(legacy.ID) != DefaultConnectionLimits() || s.DeviceConnectionLimits(custom.ID) != (ConnectionLimits{12, 4, 0, 0}) {
		t.Fatal("saving defaults changed existing devices")
	}
	if err := s.Activate(pending.ID, pendingToken, "fixture"); err != nil {
		t.Fatal(err)
	}
	if s.DeviceConnectionLimits(pending.ID) != DefaultConnectionLimits() {
		t.Fatal("defaults retroactively changed an enrollment")
	}
	fresh, freshToken, _ := s.CreatePendingDevice("new pending")
	if fresh.ConnectionLimits != defaults {
		t.Fatal("new enrollment did not use defaults", fresh.ConnectionLimits)
	}
	before := s.Snapshot()
	bulk := ConnectionLimits{48, 16, 0, 0}
	result, err = s.SetConnectionLimitDefaults(bulk, true)
	if err != nil || result.DeviceCount != 3 || result.AppliedCount != 3 || result.Defaults != bulk {
		t.Fatal(result, err)
	}
	after := s.Snapshot()
	for i, d := range before.Devices {
		d.ConnectionLimits = bulk
		if d != after.Devices[i] {
			t.Fatal("bulk update changed identity or metadata", d.ID)
		}
	}
	if !reflect.DeepEqual(before.Mappings, after.Mappings) || after.Mappings[0] != mapping {
		t.Fatal("bulk update changed mappings")
	}
	if err := s.Activate(fresh.ID, freshToken, "fixture"); err != nil {
		t.Fatal(err)
	}
	if s.DeviceConnectionLimits(fresh.ID) != bulk {
		t.Fatal("pending enrollment missed bulk change")
	}
	for id, token := range map[string]string{legacy.ID: legacyToken, custom.ID: customToken, fresh.ID: freshToken} {
		if _, valid := s.Authenticate(id, token); !valid {
			t.Fatal("credentials changed", id)
		}
	}
	if err := s.SetDeviceConnectionLimits(custom.ID, ConnectionLimits{16, 4, 0, 0}); err != nil {
		t.Fatal(err)
	}
	newDevice, _, _ := s.CreateDevice("router added later")
	if newDevice.ConnectionLimits != bulk {
		t.Fatal("new device missed global defaults")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ConnectionLimitDefaults().Defaults != bulk || reopened.Snapshot().DefaultConnectionLimits != bulk || reopened.DeviceConnectionLimits(custom.ID) != (ConnectionLimits{16, 4, 0, 0}) {
		t.Fatal("defaults or override not persisted")
	}
}

func TestConnectionLimitBulkSaveFailurePreservesDefaultsDevicesAndEnrollments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	d, token, _ := s.CreateDevice("router")
	pending, pendingToken, _ := s.CreatePendingDevice("pending")
	before := s.Snapshot()
	diskBefore, _ := os.ReadFile(path)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocked, "state.json")
	if _, err := s.SetConnectionLimitDefaults(ConnectionLimits{32, 8, 0, 0}, true); err == nil {
		t.Fatal("expected save failure")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("failed bulk update changed live settings")
	}
	if got, valid := s.Authenticate(pending.ID, pendingToken); !valid || got.ConnectionLimits != pending.ConnectionLimits {
		t.Fatal("failed bulk update changed pending enrollment")
	}
	diskAfter, _ := os.ReadFile(path)
	if !bytes.Equal(diskBefore, diskAfter) {
		t.Fatal("failed bulk update changed persisted settings")
	}
	s.path = path
	if err := s.Activate(pending.ID, pendingToken, "fixture"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.ConnectionLimitDefaults().Defaults != before.DefaultConnectionLimits || reopened.DeviceConnectionLimits(d.ID) != d.ConnectionLimits || reopened.DeviceConnectionLimits(pending.ID) != pending.ConnectionLimits {
		t.Fatal("failed settings persisted", err)
	}
	if _, valid := reopened.Authenticate(d.ID, token); !valid {
		t.Fatal("credentials changed")
	}
}

package config

import (
	"path/filepath"
	"testing"
)

func TestPluginVersionPersistsAndLegacyClientRemainsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	device, token, err := store.CreatePendingDevice("test")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ActivateWithPackage(device.ID, token, "0.1.0", "0.1.0-3"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot().Devices[0].PackageVersion; got != "0.1.0-3" {
		t.Fatal("plugin version lost", got)
	}
	if err = restored.Activate(device.ID, token, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot().Devices[0].PackageVersion; got != "" {
		t.Fatal("legacy client must not reuse old plugin report", got)
	}
}

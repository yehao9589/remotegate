package main

import (
	"encoding/json"
	"github.com/local/remotegate/internal/buildinfo"
	"path/filepath"
	"testing"
)

func TestVersionAvailableWhenInstallerIsMissing(t *testing.T) {
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/admin/version", "", "", nil), 409)
	created := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, created, 200)
	cookie := created.Result().Cookies()[0]
	w := authCall(a, "/api/admin/version", "", "", cookie)
	requireStatus(t, w, 200)
	var info buildinfo.Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != buildinfo.Version() || info.PackageVersion != buildinfo.PackageVersion() {
		t.Fatal("wrong build version", info)
	}
	t.Setenv("INSTALLER_PATH", filepath.Join(t.TempDir(), "missing.run"))
	w = authCall(a, "/api/admin/package", "", "", cookie)
	requireStatus(t, w, 200)
	var p struct {
		Available bool
		Server    buildinfo.Info
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Available || p.Server != info {
		t.Fatal("missing installer must not hide server version")
	}
	requireStatus(t, authCall(a, "/api/admin/version", "{}", "", cookie), 405)
}

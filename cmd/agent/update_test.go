package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

func updateFixture(t *testing.T, body string) (Config, update.Manifest) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/update/package" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-RemoteGate-Device-ID") != "router" {
			t.Error("invalid authenticated download", r.URL.Path)
			http.Error(w, "denied", 401)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	hash := sha256.Sum256([]byte(body))
	return Config{ServerURL: server.URL, DeviceID: "router", Token: "secret"}, update.Manifest{PackageName: "luci-app-remotegate", PackageVersion: "0.1.8-1", AgentVersion: "0.1.8", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body)), Architectures: []string{"x86_64", "ARM64", "ARMv7"}}
}

func TestDownloadIntegrityAndRedirectRejection(t *testing.T) {
	cfg, manifest := updateFixture(t, "verified installer")
	if err := downloadUpdate(context.Background(), cfg, manifest, filepath.Join(t.TempDir(), "good.run")); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*update.Manifest){func(m *update.Manifest) { m.SHA256 = strings.Repeat("0", 64) }, func(m *update.Manifest) { m.Size-- }, func(m *update.Manifest) { m.Size++ }} {
		m := manifest
		change(&m)
		if err := downloadUpdate(context.Background(), cfg, m, filepath.Join(t.TempDir(), "bad.run")); err == nil {
			t.Fatal("accepted corrupt package")
		}
	}
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		t.Error("must not forward credentials on redirect")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	cfg.ServerURL = source.URL
	if _, err := updateRequest(context.Background(), cfg, "/api/agent/update"); err == nil || redirected {
		t.Fatal("followed update redirect")
	}
}

func TestUpdateRequiresVerifiedTLSForPublicServers(t *testing.T) {
	cfg := Config{DeviceID: "router", Token: "secret"}
	for _, address := range []string{"http://console.example.com", "https://user:pass@console.example.com", "https://console.example.com/path", "https://console.example.com?x=1"} {
		cfg.ServerURL = address
		if _, err := updateBase(cfg); err == nil {
			t.Fatal("accepted", address)
		}
	}
	cfg.ServerURL = "https://console.example.com"
	cfg.InsecureTLS = true
	if _, err := updateBase(cfg); err == nil {
		t.Fatal("insecure update TLS accepted")
	}
	cfg.InsecureTLS = false
	if _, err := updateBase(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ServerURL = "http://10.0.2.2:18088"
	if _, err := updateBase(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted HTTPS must not receive credentials") }))
	defer server.Close()
	cfg.ServerURL = server.URL
	if _, err := updateRequest(context.Background(), cfg, "/api/agent/update"); err == nil {
		t.Fatal("accepted untrusted TLS")
	}
}

func TestUpdatePreservesConfigurationAndRestartsAfterInstallationFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "install failure"}[fail], func(t *testing.T) {
			cfg, manifest := updateFixture(t, "installer")
			dir := t.TempDir()
			paths := updatePaths{StateDir: filepath.Join(dir, "state"), Configuration: filepath.Join(dir, "uci"), Version: filepath.Join(dir, "version")}
			original := []byte("config remotegate 'main'\n option device_id 'same-device'\n option token 'same-token'\n option interface 'wan'\n option enabled '1'\n")
			os.WriteFile(paths.Configuration, original, 0600)
			os.WriteFile(paths.Version, []byte("0.1.7-1\n"), 0644)
			restarted := false
			err := performUpdate(context.Background(), updatePlan{Config: cfg, Manifest: manifest, State: update.Status{ID: "test", StartedAt: time.Now(), ToVersion: manifest.PackageVersion}}, dir, paths, func(ctx context.Context, path string) error {
				raw, _ := os.ReadFile(path)
				if string(raw) != "installer" {
					t.Fatal("executed unverified payload")
				}
				os.WriteFile(paths.Configuration, []byte("overwritten defaults"), 0600)
				if fail {
					return errors.New("opkg failure")
				}
				return os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0644)
			}, func(enabled bool) error {
				restarted = true
				if !enabled {
					t.Error("service enable flag lost")
				}
				raw, _ := os.ReadFile(paths.Configuration)
				if string(raw) != string(original) {
					t.Error("configuration not restored before restart")
				}
				return nil
			}, true)
			if (err != nil) != fail || !restarted {
				t.Fatal(err, restarted)
			}
			status := readUpdateState(paths)
			want := "succeeded"
			if fail {
				want = "failed"
			}
			if status.Phase != want {
				t.Fatal(status)
			}
			raw, _ := os.ReadFile(paths.Configuration)
			if string(raw) != string(original) {
				t.Fatal("lost UCI configuration")
			}
		})
	}
}

func TestFailedVerificationAndDowngradeNeverInstall(t *testing.T) {
	for _, bad := range []string{"checksum", "downgrade"} {
		t.Run(bad, func(t *testing.T) {
			cfg, manifest := updateFixture(t, "installer")
			dir := t.TempDir()
			paths := updatePaths{StateDir: filepath.Join(dir, "state"), Configuration: filepath.Join(dir, "uci"), Version: filepath.Join(dir, "version")}
			os.WriteFile(paths.Configuration, []byte("original"), 0600)
			os.WriteFile(paths.Version, []byte("0.1.7-1"), 0644)
			if bad == "checksum" {
				manifest.SHA256 = strings.Repeat("0", 64)
			} else {
				os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0644)
			}
			err := performUpdate(context.Background(), updatePlan{Config: cfg, Manifest: manifest}, dir, paths, func(context.Context, string) error { t.Fatal("installer executed"); return nil }, func(bool) error { t.Fatal("service restarted"); return nil }, false)
			if err == nil {
				t.Fatal("update not rejected")
			}
			raw, _ := os.ReadFile(paths.Configuration)
			if string(raw) != "original" {
				t.Fatal("configuration changed")
			}
		})
	}
}

func TestUpdateLockAndExpiredCommands(t *testing.T) {
	paths := updatePaths{StateDir: filepath.Join(t.TempDir(), "state")}
	if err := acquireUpdate(paths); err != nil {
		t.Fatal(err)
	}
	if acquireUpdate(paths) == nil {
		t.Fatal("concurrent update acquired lock")
	}
	old := time.Now().Add(-11 * time.Minute)
	os.Chtimes(filepath.Join(paths.StateDir, "lock"), old, old)
	if err := acquireUpdate(paths); err != nil {
		t.Fatal("stale lock not recoverable", err)
	}
	releaseUpdate(paths)
	response := updateControl(context.Background(), Config{}, protocol.Message{Type: "update-start", ID: "expired", Deadline: time.Now().Add(-time.Second).UnixMilli(), Body: []byte(`{}`)})
	if response.Error == "" || response.ID != "expired" {
		t.Fatal("expired remote command accepted")
	}
}

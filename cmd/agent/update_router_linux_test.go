//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/update"
)

// Destructive fault injection is restricted to the opted-in local QEMU router.
// Never run this against a production router or a host container.
func TestRouterAutomaticRollback(t *testing.T) {
	if os.Getenv("REMOTEGATE_UPDATE_ROUTER_TEST") != "1" {
		t.Skip("isolated iStoreOS VM only")
	}
	raw, err := os.ReadFile("/var/run/remotegate/agent.json")
	if err != nil {
		t.Fatal("missing VM agent configuration")
	}
	var cfg Config
	if json.Unmarshal(raw, &cfg) != nil || cfg.ServerURL != "http://10.0.2.2:18088" {
		t.Fatal("not the isolated local router")
	}
	original, err := os.ReadFile(routerUpdatePaths.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	version := installedPackage(routerUpdatePaths)
	oldHash, err := fileSHA256("/usr/sbin/remotegate-agent")
	if err != nil {
		t.Fatal(err)
	}
	wasEnabled := exec.Command(routerUpdatePaths.Service, "enabled").Run() == nil
	for _, failure := range []string{"partial-install", "crashed-client", "cannot-connect"} {
		t.Run(failure, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "remotegate-recovery-vm-")
			if err != nil {
				t.Fatal(err)
			}
			paths := routerUpdatePaths
			// Keep real user-visible update records separate from test results.
			paths.StateDir = filepath.Join(dir, "state")
			var backup updateBackup
			newHealthChecked, authenticationFailure := false, false
			restart := func(ctx context.Context, enabled bool) error {
				verb := "disable"
				if enabled {
					verb = "enable"
				}
				if err := runUpdateService(ctx, paths.Service, verb); err != nil {
					return err
				}
				return runUpdateService(ctx, paths.Service, "restart")
			}
			t.Cleanup(func() {
				if backup.Package == "" {
					_ = os.RemoveAll(dir)
					return
				}
				current, _ := fileSHA256("/usr/sbin/remotegate-agent")
				if current != oldHash || installedPackage(paths) != version {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					if err := backup.restore(ctx, filepath.Join(paths.StateDir, "rescue.log")); err != nil {
						t.Error("VM rescue failed", err)
						return
					}
					_ = restoreUpdateConfiguration(paths.Configuration, original)
					_ = restart(ctx, wasEnabled)
				}
				_ = os.RemoveAll(dir)
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+cfg.Token || r.Header.Get("X-RemoteGate-Device-ID") != cfg.DeviceID {
					http.Error(w, "denied", 401)
					return
				}
				_, _ = w.Write([]byte("verified local fault fixture"))
			}))
			defer server.Close()
			downloadCfg := cfg
			downloadCfg.ServerURL = server.URL
			hash := sha256.Sum256([]byte("verified local fault fixture"))
			manifest := update.Manifest{PackageName: "luci-app-remotegate", PackageVersion: strings.Split(version, "-")[0] + "-99", AgentVersion: strings.Split(version, "-")[0], Size: int64(len("verified local fault fixture")), SHA256: hex.EncodeToString(hash[:])}
			recovery := updateRecovery{
				Backup: func() error {
					var err error
					backup, err = backupInstalledPlugin("/", dir, version, manifest.Size)
					return err
				},
				Restore: func(ctx context.Context) error {
					return backup.restore(ctx, filepath.Join(paths.StateDir, "install.log"))
				},
				Verify: func(ctx context.Context, since time.Time) error {
					timeout := 90 * time.Second
					if installedPackage(paths) != version {
						newHealthChecked = true
						timeout = 10 * time.Second
					}
					err := verifyUpdatedConnection(ctx, "/", cfg, since, timeout, 2*time.Second, 500*time.Millisecond)
					if installedPackage(paths) != version {
						state, _ := readConnectionSnapshot(routerConnectionPath)
						authenticationFailure = strings.Contains(state.LastError, "401 Unauthorized")
					}
					return err
				},
			}
			started := time.Now()
			installCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err = performUpdate(installCtx, updatePlan{Config: downloadCfg, Manifest: manifest, State: update.Status{ID: "vm-fault", StartedAt: started, FromVersion: version, ToVersion: manifest.PackageVersion}}, dir, paths, func(ctx context.Context, _ string) error {
				if failure == "partial-install" {
					_ = exec.CommandContext(ctx, paths.Service, "stop").Run()
					_ = os.WriteFile("/usr/sbin/remotegate-agent", []byte("#!/bin/sh\nexit 42\n"), 0755)
					_ = os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0644)
					return errors.New("injected partial installation failure")
				}
				// Build a real opkg package containing a broken client or generator.
				raw, err := os.ReadFile(backup.Package)
				if err != nil {
					return err
				}
				outer, _ := unpackTestArchive(t, raw)
				data, modes := unpackTestArchive(t, outer["data.tar.gz"])
				control, controlModes := unpackTestArchive(t, outer["control.tar.gz"])
				data["usr/share/remotegate/version"] = []byte(manifest.PackageVersion + "\n")
				control["control"] = []byte(strings.Replace(string(control["control"]), "Version: "+version+"\n", "Version: "+manifest.PackageVersion+"\n", 1))
				if failure == "crashed-client" {
					data["usr/sbin/remotegate-agent"] = []byte("#!/bin/sh\nexit 42\n")
				} else {
					generator := string(data["usr/libexec/remotegate-config"])
					if !strings.Contains(generator, "token=c.token") {
						return errors.New("unsupported generator fixture")
					}
					data["usr/libexec/remotegate-config"] = []byte(strings.Replace(generator, "token=c.token", `token="invalid-local-test-credential"`, 1))
				}
				for _, group := range []struct {
					name  string
					files map[string][]byte
					modes map[string]int64
				}{{"data.tar.gz", data, modes}, {"control.tar.gz", control, controlModes}} {
					var entries []backupEntry
					for name, content := range group.files {
						file, err := os.CreateTemp(dir, "entry-")
						if err != nil {
							return err
						}
						_, err = file.Write(content)
						_ = file.Close()
						if err != nil {
							return err
						}
						entries = append(entries, backupEntry{name, file.Name(), group.modes[name], int64(len(content))})
					}
					out := filepath.Join(dir, "fault-"+group.name)
					if err := writeBackupArchive(out, entries); err != nil {
						return err
					}
					outer[group.name], err = os.ReadFile(out)
					if err != nil {
						return err
					}
				}
				var entries []backupEntry
				for name, content := range outer {
					file, err := os.CreateTemp(dir, "ipk-entry-")
					if err != nil {
						return err
					}
					_, _ = file.Write(content)
					_ = file.Close()
					entries = append(entries, backupEntry{name, file.Name(), 0600, int64(len(content))})
				}
				ipk := filepath.Join(dir, "fault.ipk")
				if err := writeBackupArchive(ipk, entries); err != nil {
					return err
				}
				command := exec.CommandContext(ctx, "opkg", "--force-reinstall", "install", ipk)
				prepareUpdateInstaller(command)
				if err := command.Run(); err != nil {
					return errors.New("fault package installation failed")
				}
				return nil
			}, restart, wasEnabled, recovery)
			state := readUpdateState(paths)
			if err == nil || state.Phase != "failed" || !state.RolledBack || !state.RollbackVerified || state.RollbackFailed {
				if log, readErr := os.ReadFile(filepath.Join(paths.StateDir, "install.log")); readErr == nil {
					t.Logf("opkg log: %s", log)
				}
				t.Fatal("automatic recovery failed", err, state)
			}
			if failure != "partial-install" && !newHealthChecked {
				t.Fatal("fixture never reached new client health verification", state.Message)
			}
			if failure == "cannot-connect" && !authenticationFailure {
				t.Fatal("fixture did not exercise an authenticated connection failure", state.Message)
			}
			got, _ := os.ReadFile(paths.Configuration)
			current, _ := fileSHA256("/usr/sbin/remotegate-agent")
			if string(got) != string(original) || current != oldHash || installedPackage(paths) != version {
				t.Fatal("VM identity or old plugin changed")
			}
			if (exec.Command(paths.Service, "enabled").Run() == nil) != wasEnabled {
				t.Fatal("boot enable setting changed")
			}
			t.Logf("%s: actual opkg rollback, old binary/configuration/boot setting and authenticated heartbeat verified (%s)", failure, time.Since(started).Round(time.Second))
		})
	}
}

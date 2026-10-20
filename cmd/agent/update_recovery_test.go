package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/update"
)

func TestUpdateFailureRecovery(t *testing.T) {
	for _, failure := range []string{"install", "restart", "version", "heartbeat", "deadline", "restore", "old-network", "backup", "prepare-deadline"} {
		t.Run(failure, func(t *testing.T) {
			cfg, manifest := updateFixture(t, "installer")
			dir := t.TempDir()
			paths := updatePaths{StateDir: filepath.Join(dir, "state"), Configuration: filepath.Join(dir, "uci"), Version: filepath.Join(dir, "version")}
			original := []byte("original identity and connection settings")
			_ = os.WriteFile(paths.Configuration, original, 0600)
			_ = os.WriteFile(paths.Version, []byte("0.1.7-1"), 0644)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			installs, restarts, restores, verifications := 0, 0, 0, 0
			recovery := updateRecovery{
				Backup: func() error {
					if failure == "prepare-deadline" {
						cancel()
					}
					if failure == "backup" {
						return errors.New("backup unavailable")
					}
					return nil
				},
				Restore: func(ctx context.Context) error {
					restores++
					if ctx.Err() != nil {
						t.Error("recovery reused cancelled installation context")
					}
					if readUpdateState(paths).Phase != "rolling_back" {
						t.Error("missing rollback stage")
					}
					if failure == "restore" {
						return errors.New("disk error")
					}
					return os.WriteFile(paths.Version, []byte("0.1.7-1"), 0644)
				},
				Verify: func(ctx context.Context, since time.Time) error {
					verifications++
					if failure == "old-network" || installedPackage(paths) == manifest.PackageVersion {
						return errors.New("heartbeat unavailable")
					}
					return nil
				},
			}
			err := performUpdate(ctx, updatePlan{Config: cfg, Manifest: manifest, State: update.Status{ID: "recovery", StartedAt: time.Now()}}, dir, paths,
				func(ctx context.Context, name string) error {
					installs++
					_ = os.WriteFile(paths.Configuration, []byte("overwritten"), 0600)
					_ = os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0644)
					if failure == "install" {
						return errors.New("partial install")
					}
					if failure == "version" {
						_ = os.WriteFile(paths.Version, []byte("0.1.7-9"), 0644)
					}
					if failure == "deadline" {
						cancel()
						return ctx.Err()
					}
					return nil
				}, func(ctx context.Context, enabled bool) error {
					restarts++
					if enabled {
						t.Error("disabled boot setting changed")
					}
					got, _ := os.ReadFile(paths.Configuration)
					if string(got) != string(original) {
						t.Error("identity lost before restart")
					}
					if failure == "restart" && restarts == 1 {
						return errors.New("start failure")
					}
					return nil
				}, false, recovery)
			if err == nil {
				t.Fatal("failed update reported success")
			}
			state := readUpdateState(paths)
			if state.Phase != "failed" || state.Active() || !state.AutoRollback {
				t.Fatal(state)
			}
			if failure == "backup" || failure == "prepare-deadline" {
				if installs != 0 || restores != 0 || restarts != 0 {
					t.Fatal("backup failure modified plugin")
				}
				return
			}
			if restores != 1 || state.RolledBack != (failure != "restore") || state.RollbackFailed != (failure == "restore") {
				t.Fatal(state, restores)
			}
			if failure != "restore" && installedPackage(paths) != "0.1.7-1" {
				t.Fatal("old version not restored")
			}
			if failure == "old-network" && (state.RollbackVerified || !strings.Contains(state.Message, "连接尚未恢复")) {
				t.Fatal("network outage claimed healthy", state)
			}
			got, _ := os.ReadFile(paths.Configuration)
			if string(got) != string(original) {
				t.Fatal("configuration lost")
			}
		})
	}
}

func TestUpdateSuccessWaitsForVerification(t *testing.T) {
	cfg, manifest := updateFixture(t, "installer")
	dir := t.TempDir()
	paths := updatePaths{StateDir: filepath.Join(dir, "state"), Configuration: filepath.Join(dir, "uci"), Version: filepath.Join(dir, "version")}
	_ = os.WriteFile(paths.Configuration, []byte("original"), 0600)
	_ = os.WriteFile(paths.Version, []byte("0.1.7-1"), 0644)
	recovery := testUpdateRecovery(paths, "0.1.7-1")
	recovery.Restore = func(context.Context) error { t.Fatal("healthy update rolled back"); return nil }
	recovery.Verify = func(context.Context, time.Time) error {
		if state := readUpdateState(paths); state.Phase != "verifying" || !state.Active() {
			t.Fatal("premature completion", state)
		}
		return nil
	}
	err := performUpdate(context.Background(), updatePlan{Config: cfg, Manifest: manifest, State: update.Status{StartedAt: time.Now()}}, dir, paths, func(context.Context, string) error {
		return os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0644)
	}, func(context.Context, bool) error { return nil }, true, recovery)
	if err != nil || readUpdateState(paths).Phase != "succeeded" {
		t.Fatal(err, readUpdateState(paths))
	}
}

func TestUpdatedProcessRejectsStaleOrUnrelatedConnection(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DeviceID: "router", ServerURL: "https://console.example.com"}
	proc := filepath.Join(root, "proc", "42")
	_ = os.MkdirAll(proc, 0700)
	_ = os.WriteFile(filepath.Join(proc, "cmdline"), []byte("/usr/sbin/remotegate-agent\x00/var/run/remotegate/agent.json\x00"), 0600)
	_ = os.WriteFile(filepath.Join(proc, "exe"), []byte("expected binary"), 0600)
	hash, _ := fileSHA256(filepath.Join(proc, "exe"))
	now := time.Now().Truncate(time.Second)
	since := now.Add(-40 * time.Second)
	valid := connectionSnapshot{PID: 42, Server: cfg.ServerURL, DeviceID: cfg.DeviceID, State: "connected", ConnectedAt: connectionTime(since.Add(time.Second)), LastHeartbeatAt: connectionTime(now.Add(-time.Second)), UpdatedUnix: now.Unix()}
	if !healthyUpdatedProcess(root, cfg, since, now, hash, valid) {
		t.Fatal("valid process rejected")
	}
	for _, change := range []func(*connectionSnapshot){
		func(s *connectionSnapshot) { s.PID = 43 }, func(s *connectionSnapshot) { s.State = "reconnecting" }, func(s *connectionSnapshot) { s.DeviceID = "other" }, func(s *connectionSnapshot) { s.Server = "https://other.example.com" }, func(s *connectionSnapshot) { s.ConnectedAt = connectionTime(since.Add(-time.Second)) }, func(s *connectionSnapshot) { s.LastHeartbeatAt = "" }, func(s *connectionSnapshot) { s.LastHeartbeatAt = connectionTime(now.Add(-time.Minute)) }, func(s *connectionSnapshot) { s.UpdatedUnix = now.Add(-time.Minute).Unix() },
	} {
		state := valid
		change(&state)
		if healthyUpdatedProcess(root, cfg, since, now, hash, state) {
			t.Fatal("invalid connection accepted", state)
		}
	}
	if healthyUpdatedProcess(root, cfg, since, now, "different executable", valid) {
		t.Fatal("old executable accepted")
	}
	_ = os.WriteFile(filepath.Join(proc, "cmdline"), []byte("unrelated process"), 0600)
	if healthyUpdatedProcess(root, cfg, since, now, hash, valid) {
		t.Fatal("unrelated process accepted")
	}
}

func TestConnectionHealthNeedsStableWindow(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DeviceID: "router", ServerURL: "https://console.example.com"}
	for _, dir := range []string{"proc/42", "usr/sbin", "var/run/remotegate"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0700)
	}
	_ = os.WriteFile(filepath.Join(root, "usr/sbin/remotegate-agent"), []byte("binary"), 0600)
	_ = os.WriteFile(filepath.Join(root, "proc/42/exe"), []byte("binary"), 0600)
	_ = os.WriteFile(filepath.Join(root, "proc/42/cmdline"), []byte("/usr/sbin/remotegate-agent\x00/var/run/remotegate/agent.json\x00"), 0600)
	now := time.Now()
	state := connectionSnapshot{PID: 42, Server: cfg.ServerURL, DeviceID: cfg.DeviceID, State: "connected", ConnectedAt: connectionTime(now.Add(-2 * time.Second)), LastHeartbeatAt: connectionTime(now.Add(-time.Second)), UpdatedUnix: now.Unix()}
	raw, _ := json.Marshal(state)
	_ = os.WriteFile(filepath.Join(root, "var/run/remotegate/connection.json"), raw, 0600)
	if err := verifyUpdatedConnection(context.Background(), root, cfg, now.Add(-3*time.Second), 200*time.Millisecond, 25*time.Millisecond, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpdatedConnection(context.Background(), root, cfg, now.Add(-3*time.Second), 20*time.Millisecond, time.Second, 5*time.Millisecond); err == nil {
		t.Fatal("short connection passed stability gate")
	}
}

package main

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConnectionReportTracksHandshakeHeartbeatFailureAndRecovery(t *testing.T) {
	path := t.TempDir() + "/connection.json"
	token := "sensitive-device-token"
	r := newConnectionReporter(path, Config{ServerURL: "https://user:password@example.com/?secret=hidden", DeviceID: "device", Token: token})
	read := func() connectionSnapshot {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "hidden") {
			t.Fatal("Credentials stored in status")
		}
		var value connectionSnapshot
		if err = json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	r.attempting()
	if read().State != "connecting" {
		t.Fatal(read())
	}
	r.resolution("192.0.2.1", "encrypted")
	r.route("pppoe-wan", true)
	r.connected()
	r.heartbeat()
	value := read()
	if value.State != "connected" || value.ConnectedAt == "" || value.LastHeartbeatAt == "" || value.ServerIP != "192.0.2.1" || value.DNSMode != "encrypted" {
		t.Fatal(value)
	}
	if value.EgressInterface != "pppoe-wan" || !value.RouteProtected {
		t.Fatal("missing route policy status", value)
	}
	r.disconnected(errors.New("connect failed token="+token), time.Second)
	value = read()
	if value.State != "reconnecting" || value.NextRetryAt == "" || value.LastErrorAt == "" || !strings.Contains(value.LastError, "[redacted]") {
		t.Fatal(value)
	}
	r.attempting()
	if read().State != "reconnecting" {
		t.Fatal(read())
	}
	r.connected()
	if read().State != "connected" || read().NextRetryAt != "" {
		t.Fatal(read())
	}
	r.stopped()
	if read().State != "stopped" {
		t.Fatal(read())
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("Status must remain private", info, err)
	}
}

func TestConcurrentConnectionStatusNeverPublishesPartialJSON(t *testing.T) {
	path := t.TempDir() + "/connection.json"
	r := newConnectionReporter(path, Config{ServerURL: "https://example.com", DeviceID: "test"})
	r.connected()
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 25; j++ {
				r.heartbeat()
				raw, err := os.ReadFile(path)
				if err != nil || !json.Valid(raw) {
					t.Errorf("Partial connection snapshot: %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()
}

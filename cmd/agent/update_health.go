package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type updateRecovery struct {
	Backup  func() error
	Restore func(context.Context) error
	Verify  func(context.Context, time.Time) error
}

func readConnectionSnapshot(name string) (connectionSnapshot, error) {
	var state connectionSnapshot
	f, err := os.Open(name)
	if err != nil {
		return state, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(raw) > 8192 {
		return state, errors.New("连接状态文件无效")
	}
	err = json.Unmarshal(raw, &state)
	return state, err
}

// Require a fresh process, the installed executable, the same configured
// identity, and a live authenticated tunnel with a recent server heartbeat.
func healthyUpdatedProcess(root string, cfg Config, since, now time.Time, expectedHash string, state connectionSnapshot) bool {
	connected, err := time.Parse(time.RFC3339, state.ConnectedAt)
	if err != nil || connected.Before(since.Truncate(time.Second)) || connected.After(now) {
		return false
	}
	heartbeat, err := time.Parse(time.RFC3339, state.LastHeartbeatAt)
	if err != nil || !heartbeat.After(connected) || heartbeat.After(now) || now.Sub(heartbeat) > 45*time.Second {
		return false
	}
	if state.State != "connected" || state.PID <= 1 || state.DeviceID != cfg.DeviceID || strings.TrimRight(state.Server, "/") != strings.TrimRight(cfg.ServerURL, "/") || now.Unix()-state.UpdatedUnix > 45 || state.UpdatedUnix > now.Unix()+5 {
		return false
	}
	proc := filepath.Join(root, "proc", fmt.Sprint(state.PID))
	cmd, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil || !(bytes.Equal(cmd, []byte("/usr/sbin/remotegate-agent\x00/var/run/remotegate/agent.json\x00")) || bytes.Equal(cmd, []byte("/usr/sbin/remotegate-agent\x00/etc/remotegate/agent.json\x00"))) {
		return false
	}
	actual, err := fileSHA256(filepath.Join(proc, "exe"))
	return err == nil && actual == expectedHash
}

func verifyUpdatedConnection(ctx context.Context, root string, cfg Config, since time.Time, timeout, stableFor, poll time.Duration) error {
	expected, err := fileSHA256(filepath.Join(root, "usr/sbin/remotegate-agent"))
	if err != nil {
		return errors.New("更新后的客户端文件不可读")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var stable time.Time
	var process int
	var session string
	for {
		now := time.Now()
		state, err := readConnectionSnapshot(filepath.Join(root, "var/run/remotegate/connection.json"))
		if err == nil && healthyUpdatedProcess(root, cfg, since, now, expected, state) {
			if stable.IsZero() || process != state.PID || session != state.ConnectedAt {
				stable, process, session = now, state.PID, state.ConnectedAt
			}
			if now.Sub(stable) >= stableFor {
				return nil
			}
		} else {
			stable = time.Time{}
		}
		select {
		case <-ctx.Done():
			return errors.New("客户端未在规定时间内恢复稳定连接和服务器心跳")
		case <-ticker.C:
		}
	}
}

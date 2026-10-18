package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/traffic"
)

type deviceView struct {
	config.Device
	Online          bool          `json:"online"`
	ConnectedAt     time.Time     `json:"connectedAt,omitempty"`
	CanUpdate       bool          `json:"canUpdate"`
	CanRemote       bool          `json:"canRemote"`
	CanManageUpdate bool          `json:"canManageUpdate"`
	LastActiveAt    time.Time     `json:"lastActiveAt,omitempty"`
	LatencyAt       time.Time     `json:"latencyAt,omitempty"`
	LatencyMs       *int64        `json:"latencyMs,omitempty"`
	Network         deviceNetwork `json:"network"`
}

func (a *app) viewDevice(d config.Device, s hub.Status, online bool) deviceView {
	d.TokenHash = ""
	return deviceView{Device: d, Online: online, ConnectedAt: s.Since, CanUpdate: s.CanUpdate, CanRemote: s.CanRemote, CanManageUpdate: s.CanManageUpdate, LastActiveAt: s.LastActiveAt, LatencyAt: s.LatencyAt, LatencyMs: s.LatencyMs, Network: a.operators.Lookup(d.Info.PeerIP)}
}

func (a *app) deviceConnectionEvents(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	events, ok := a.store.DeviceConnectionEvents(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"events": events, "limit": config.ConnectionEventLimit, "checkedAt": time.Now().UTC()})
}

// Refresh only this device. A probe timeout must not be reported as a definite
// disconnect: the heartbeat/read loop owns the live connection lifecycle.
func (a *app) refreshDeviceStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := a.store.DeviceConnectionEvents(id); !ok {
		http.NotFound(w, r)
		return
	}
	before, online := a.hub.Statuses()[id]
	probe := "offline"
	if online {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		_, err := a.hub.Ping(ctx, id)
		cancel()
		if r.Context().Err() != nil {
			return
		}
		probe = "ok"
		if err != nil {
			probe = "unresponsive"
		}
	}
	status, online := a.hub.Statuses()[id]
	if !online {
		probe = "offline"
	} else if !status.Since.Equal(before.Since) {
		probe = "changed"
	}
	for _, d := range a.store.Snapshot().Devices {
		if d.ID != id {
			continue
		}
		counter := traffic.Counter{}
		if a.traffic != nil {
			counter = a.traffic.Snapshot().Devices[id]
		}
		writeJSON(w, map[string]any{"device": a.viewDevice(d, status, online), "traffic": counter, "probe": probe, "checkedAt": time.Now().UTC()})
		return
	}
	http.NotFound(w, r)
}

func deviceConnectionInfo(r *http.Request) config.DeviceInfo {
	info := config.DeviceInfo{}
	if r.URL.Query().Get("info") == "1" {
		info.OS, info.Arch = r.URL.Query().Get("os"), r.URL.Query().Get("arch")
		info.Hostname, info.Interface = r.URL.Query().Get("hostname"), r.URL.Query().Get("interface")
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Only a local reverse proxy can supply the connection source. Never accept
	// a router's own query parameter or an Internet peer's forwarded header.
	if loopbackPeer(r) {
		if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(value) != nil {
			peer = value
		}
	}
	if ip := net.ParseIP(peer); ip != nil {
		info.PeerIP = ip.String()
	}
	return info
}

func (a *app) deviceConnectionAction(w http.ResponseWriter, r *http.Request, id, operation string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	found := false
	var device config.Device
	for _, d := range a.store.Snapshot().Devices {
		if d.ID == id {
			found = true
			device = d
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	status, online := a.hub.Statuses()[id]
	if !online {
		http.Error(w, "设备未连接，无法执行此操作", http.StatusConflict)
		return
	}
	if operation == "reconnect" {
		a.updateMu.Lock()
		updateState := a.deviceUpdates[id].State
		// A new session reporting the requested package proves installation finished,
		// even if the administrator closed the update window before its last poll.
		updating := updateState.Active() && (updateState.ToVersion == "" || updateState.ToVersion != device.PackageVersion)
		a.updateMu.Unlock()
		if updating {
			http.Error(w, "插件正在更新，请等待更新完成后再重新连接", http.StatusConflict)
			return
		}
		if err := a.store.RecordDisconnect(id, status.Since, time.Now(), "reconnect"); err != nil {
			http.Error(w, "未能保存断开记录，请检查数据目录后重试", 500)
			return
		}
		a.hub.Disconnect(id, status.Since)
		writeJSON(w, map[string]any{"reconnecting": true, "message": "已断开当前隧道，等待客户端自动重新连接"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	latency, err := a.hub.Ping(ctx, id)
	if err != nil {
		message := "连接检测失败，请稍后重试"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "隧道响应超时，设备可能繁忙或网络不稳定；请稍后重试"
		}
		if errors.Is(err, hub.ErrOffline) {
			message = "检测期间设备连接已断开"
		}
		http.Error(w, message, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"latencyMs": latency.Milliseconds(), "checkedAt": time.Now().UTC()})
}

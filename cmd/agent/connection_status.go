package main

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const routerConnectionPath = "/var/run/remotegate/connection.json"

type connectionSnapshot struct {
	PID             int    `json:"pid"`
	Server          string `json:"server"`
	DeviceID        string `json:"deviceId"`
	State           string `json:"state"`
	ConnectedAt     string `json:"connectedAt,omitempty"`
	LastAttemptAt   string `json:"lastAttemptAt,omitempty"`
	LastHeartbeatAt string `json:"lastHeartbeatAt,omitempty"`
	NextRetryAt     string `json:"nextRetryAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	LastErrorAt     string `json:"lastErrorAt,omitempty"`
	ServerIP        string `json:"serverIP,omitempty"`
	DNSMode         string `json:"dnsMode,omitempty"`
	EgressInterface string `json:"egressInterface,omitempty"`
	RouteProtected  bool   `json:"routeProtected,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
	UpdatedUnix     int64  `json:"updatedUnix"`
}

type connectionReporter struct {
	mu     sync.Mutex
	path   string
	token  string
	state  connectionSnapshot
	warned bool
}

func newConnectionReporter(path string, cfg Config) *connectionReporter {
	server := ""
	if u, err := url.Parse(cfg.ServerURL); err == nil {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = ""
		server = strings.TrimRight(u.String(), "/")
	}
	return &connectionReporter{path: path, token: cfg.Token, state: connectionSnapshot{PID: os.Getpid(), Server: server, DeviceID: cfg.DeviceID, State: "connecting"}}
}

func connectionTime(now time.Time) string { return now.UTC().Format(time.RFC3339) }

func safeConnectionError(err error, token string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted]")
		text = strings.ReplaceAll(text, url.QueryEscape(token), "[redacted]")
	}
	if len(text) > 1500 {
		text = text[:1500]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func (r *connectionReporter) saveLocked() {
	now := time.Now()
	r.state.UpdatedAt = connectionTime(now)
	r.state.UpdatedUnix = now.Unix()
	raw, err := json.Marshal(r.state)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(r.path), 0700)
	}
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(filepath.Dir(r.path), ".connection-")
		if err == nil {
			defer os.Remove(file.Name())
			_, err = file.Write(raw)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), r.path)
			}
		}
	}
	if err != nil && !r.warned {
		log.Printf("connection status unavailable: %v", err)
		r.warned = true
	}
}

func (r *connectionReporter) attempting() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.State = "connecting"
	if r.state.LastError != "" || r.state.ConnectedAt != "" {
		r.state.State = "reconnecting"
	}
	r.state.LastAttemptAt = connectionTime(time.Now())
	r.state.NextRetryAt = ""
	r.saveLocked()
}
func (r *connectionReporter) resolution(ip, mode string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.ServerIP, r.state.DNSMode = ip, mode
	r.saveLocked()
}
func (r *connectionReporter) route(iface string, protected bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.EgressInterface, r.state.RouteProtected = iface, protected
	r.saveLocked()
}
func (r *connectionReporter) connected() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.State = "connected"
	r.state.ConnectedAt = connectionTime(time.Now())
	r.state.LastHeartbeatAt = ""
	r.state.NextRetryAt = ""
	r.saveLocked()
}
func (r *connectionReporter) heartbeat() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.State == "connected" {
		r.state.LastHeartbeatAt = connectionTime(time.Now())
		r.saveLocked()
	}
}
func (r *connectionReporter) disconnected(err error, delay time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.State = "reconnecting"
	r.state.NextRetryAt = connectionTime(time.Now().Add(delay))
	if err != nil {
		r.state.LastError = safeConnectionError(err, r.token)
		r.state.LastErrorAt = connectionTime(time.Now())
	}
	r.saveLocked()
}
func (r *connectionReporter) stopped() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.State = "stopped"
	r.state.NextRetryAt = ""
	r.saveLocked()
}

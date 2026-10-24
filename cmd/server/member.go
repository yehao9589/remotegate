package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/traffic"
)

const memberPath = "/user/devices"

func (a *app) member(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.auth == nil {
			http.Error(w, "请先登录", 401)
			return
		}
		if _, ok := a.auth.identity(r); !ok {
			http.Error(w, "登录已失效，请重新登录", 401)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !sameAuthOrigin(r) {
			http.Error(w, "请求来源不匹配", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

type memberDevice struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Online             bool        `json:"online"`
	Version            string      `json:"version"`
	CanRemote          bool        `json:"canRemote"`
	ConnectedAt        time.Time   `json:"connectedAt,omitempty"`
	LastDisconnectedAt time.Time   `json:"lastDisconnectedAt,omitempty"`
	Permissions        deviceGrant `json:"permissions"`
}
type memberMapping struct {
	ID       string `json:"id"`
	DeviceID string `json:"deviceId"`
	Host     string `json:"host"`
	URL      string `json:"url"`
	Note     string `json:"note"`
	Enabled  bool   `json:"enabled"`
}

func (a *app) memberGrants(r *http.Request) map[string]deviceGrant {
	owner, ok := a.remoteOwner(r)
	out := map[string]deviceGrant{}
	if !ok {
		return out
	}
	a.auth.mu.Lock()
	defer a.auth.mu.Unlock()
	session := a.auth.sessions[owner]
	if session.user == a.auth.record.Username {
		for _, d := range a.store.Snapshot().Devices {
			out[d.ID] = deviceGrant{d.ID, true, true}
		}
		return out
	}
	for _, u := range a.auth.record.Users {
		if u.Username == session.user {
			for _, g := range u.Grants {
				out[g.DeviceID] = g
			}
		}
	}
	return out
}
func (a *app) memberState(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	identity, _ := a.auth.identity(r)
	grants := a.memberGrants(r)
	snapshot := a.store.Snapshot()
	statuses := a.hub.Statuses()
	devices := []memberDevice{}
	mappings := []memberMapping{}
	totals := traffic.Snapshot{Devices: map[string]traffic.Counter{}, Mappings: map[string]traffic.Counter{}}
	all := totals
	if a.traffic != nil {
		all = a.traffic.Snapshot()
		totals.Since = all.Since
		totals.Version = all.Version
	}
	for _, d := range snapshot.Devices {
		if grant, ok := grants[d.ID]; ok {
			status, online := statuses[d.ID]
			devices = append(devices, toMemberDevice(d, status, online, grant))
			totals.Devices[d.ID] = all.Devices[d.ID]
		}
	}
	for _, m := range snapshot.Mappings {
		if _, ok := grants[m.DeviceID]; ok {
			port := m.PublicPort
			scheme := m.PublicScheme
			if scheme == "" {
				scheme = "https"
			}
			if port == 0 {
				port = 443
				if scheme == "http" {
					port = 80
				}
			}
			mappings = append(mappings, memberMapping{m.ID, m.DeviceID, m.Host, scheme + "://" + net.JoinHostPort(m.Host, strconv.Itoa(port)) + "/", m.Note, m.Enabled})
			totals.Mappings[m.ID] = all.Mappings[m.ID]
		}
	}
	writeJSON(w, map[string]any{"account": identity, "devices": devices, "mappings": mappings, "traffic": totals})
}
func toMemberDevice(d config.Device, status hub.Status, online bool, grant deviceGrant) memberDevice {
	version := d.PackageVersion
	if version == "" {
		version = d.Version
	}
	return memberDevice{ID: d.ID, Name: d.Name, Online: online, Version: version, CanRemote: status.CanRemote, ConnectedAt: status.Since, LastDisconnectedAt: d.LastDisconnectedAt, Permissions: grant}
}
func (a *app) memberDeviceHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/member/devices/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	owner, ok := a.remoteOwner(r)
	if !ok || !a.auth.ownerAllows(owner, parts[0], "view") {
		http.Error(w, "未获授权访问此设备", 403)
		return
	}
	switch parts[1] {
	case "events":
		a.deviceConnectionEvents(w, r, parts[0])
	case "remote":
		a.deviceRemoteConnect(w, r, parts[0])
	default:
		http.NotFound(w, r)
	}
}
func (a *app) memberTrends(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.traffic == nil {
		http.Error(w, "流量统计尚未就绪", 503)
		return
	}
	scope, id, period := r.URL.Query().Get("scope"), r.URL.Query().Get("id"), r.URL.Query().Get("period")
	if scope != "devices" && scope != "mappings" || period != "day" && period != "month" {
		http.Error(w, "统计参数无效", 400)
		return
	}
	grants := a.memberGrants(r)
	ids := []string{}
	snapshot := a.store.Snapshot()
	if scope == "devices" {
		for _, d := range snapshot.Devices {
			if _, ok := grants[d.ID]; ok && (id == "" || id == d.ID) {
				ids = append(ids, d.ID)
			}
		}
	} else {
		for _, m := range snapshot.Mappings {
			if _, ok := grants[m.DeviceID]; ok && (id == "" || id == m.ID) {
				ids = append(ids, m.ID)
			}
		}
	}
	if id != "" && len(ids) == 0 {
		http.Error(w, "未获授权或对象不存在", 403)
		return
	}
	writeJSON(w, a.traffic.Series(scope, ids, period, time.Now()))
}
func (a *app) memberPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != memberPath {
		http.NotFound(w, r)
		return
	}
	if a.auth == nil || !a.auth.initialized() {
		http.Redirect(w, r, "/", 302)
		return
	}
	if _, ok := a.auth.identity(r); !ok {
		http.Redirect(w, r, a.auth.entryPath(), 302)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	raw, _ := webFS.ReadFile("web/member.html")
	script, _ := webFS.ReadFile("web/member.js")
	remoteJS, _ := webFS.ReadFile("web/remote-access.js")
	copyJS, _ := webFS.ReadFile("web/clipboard.js")
	css, _ := webFS.ReadFile("web/member.css")
	remoteCSS, _ := webFS.ReadFile("web/remote-access.css")
	terminalCSS, _ := webFS.ReadFile("web/vendor/xterm.css")
	html := strings.Replace(string(raw), "/* MEMBER_JS */", string(copyJS)+"\n"+string(script)+"\n"+string(remoteJS), 1)
	login, _ := json.Marshal(a.auth.entryPath())
	html = strings.Replace(html, "/* MEMBER_LOGIN */\"/\"", string(login), 1)
	html = strings.Replace(html, "/* MEMBER_CSS */", string(css)+"\n"+string(remoteCSS)+"\n"+string(terminalCSS), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'")
	_, _ = w.Write([]byte(html))
}

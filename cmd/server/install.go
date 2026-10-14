package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/update"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type installTicket struct {
	Owner                   *app
	DeviceID, Token, Server string
	Expires                 time.Time
	Profile                 installProfile
	OperationID, ReportKey  string
	Packages                map[string]packageArtifact
}

var installs = struct {
	sync.Mutex
	items map[string]installTicket
}{items: make(map[string]installTicket)}

func installerPath() string {
	return env("INSTALLER_PATH", filepath.Join("dist", buildinfo.PackageFilename()))
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *app) createInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		DeviceID  string `json:"deviceId"`
		Token     string `json:"token"`
		Server    string `json:"server"`
		ProfileID string `json:"profileId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	device, ok := a.store.Authenticate(in.DeviceID, in.Token)
	if !ok {
		http.Error(w, "invalid device credentials", 400)
		return
	}
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, "安装配置无法读取", 503)
		return
	}
	profile, exists := p.profile(in.ProfileID)
	if !exists {
		http.Error(w, "安装模板不存在", 400)
		return
	}
	if in.Server == "" {
		in.Server = profile.Server
	}
	u, e := url.Parse(in.Server)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		http.Error(w, "请输入可被路由器访问的 HTTP/HTTPS 服务端地址", 400)
		return
	}
	if _, e = os.Stat(installerPath()); e != nil {
		http.Error(w, "服务端尚未部署安装包", 503)
		return
	}
	b := make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		http.Error(w, "random source unavailable", 500)
		return
	}
	key := hex.EncodeToString(b)
	name := device.Name
	for _, d := range a.store.Snapshot().Devices {
		if d.ID == in.DeviceID {
			name = d.Name
		}
	}
	targetVersion := buildinfo.PackageVersion()
	if m, err := a.loadUpdatePackage(); err == nil {
		targetVersion = m.PackageVersion
	}
	operationID, err := p.record(in.DeviceID, name, "install", update.Status{Phase: "ready", ToVersion: targetVersion, Message: "安装命令已生成 · 模板：" + profile.Name})
	if err != nil {
		http.Error(w, "安装记录保存失败", 500)
		return
	}
	reportKey := packageSecret()
	if reportKey == "" {
		http.Error(w, "安装授权生成失败", 500)
		return
	}
	t := installTicket{Owner: a, DeviceID: in.DeviceID, Token: in.Token, Server: strings.TrimRight(in.Server, "/"), Expires: time.Now().Add(15 * time.Minute), Profile: profile, OperationID: operationID, ReportKey: reportKey, Packages: make(map[string]packageArtifact)}
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		if item, err := a.packageArtifact(p, "", arch); err == nil {
			t.Packages[arch] = item
		}
	}
	p.mu.Lock()
	for key, grant := range p.installs {
		if time.Now().After(grant.Expires) {
			delete(p.installs, key)
		}
	}
	p.installs[reportKey] = installProgressGrant{operationID, in.DeviceID, time.Now().Add(30 * time.Minute)}
	p.mu.Unlock()
	installs.Lock()
	for k, v := range installs.items {
		if time.Now().After(v.Expires) {
			delete(installs.items, k)
		}
	}
	installs.items[key] = t
	installs.Unlock()
	link := t.Server + "/install/" + key
	command := "umask 077; f=$(mktemp /tmp/remotegate-setup.XXXXXX) && { curl -fSL " + shellQuote(link) + " -o \"$f\" && sh \"$f\"; }; rm -f \"$f\""
	writeJSON(w, map[string]any{"command": command, "expiresAt": t.Expires, "operationId": operationID})
}
func (a *app) installerDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.Query().Get("arch") != "" || r.URL.Query().Get("version") != "" || r.URL.Query().Get("artifact") != "" {
		p, err := a.packageCenterStore()
		if err != nil {
			http.Error(w, "安装包不可用", 503)
			return
		}
		var item packageArtifact
		if key := r.URL.Query().Get("artifact"); key != "" {
			p.mu.Lock()
			item = p.artifacts[key]
			p.mu.Unlock()
			if item.Path == "" {
				http.NotFound(w, r)
				return
			}
			err = verifyArtifact(item)
		} else {
			item, err = a.packageArtifact(p, r.URL.Query().Get("version"), r.URL.Query().Get("arch"))
		}
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		kind := item.Arch
		if kind == "" {
			kind = "istore"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "RemoteGate-" + item.Manifest.PackageVersion + "-" + kind + ".run"}))
		http.ServeFile(w, r, item.Path)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(installerPath())}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, installerPath())
}
func (a *app) installScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/install/")
	installs.Lock()
	t, ok := installs.items[key]
	if ok && t.Owner == a {
		delete(installs.items, key)
	}
	installs.Unlock()
	if !ok || t.Owner != a || time.Now().After(t.Expires) {
		http.Error(w, "安装链接已失效，请在后台重新生成", 410)
		return
	}
	if _, ok = a.store.Authenticate(t.DeviceID, t.Token); !ok {
		http.Error(w, "device removed", 410)
		return
	}
	data, e := os.ReadFile(installerPath())
	if e != nil {
		http.Error(w, "installer unavailable", 503)
		return
	}
	sum := sha256.Sum256(data)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(installScriptText(t, fmt.Sprintf("%x", sum), len(data))))
}

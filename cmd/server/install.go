package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
}

var installs = struct {
	sync.Mutex
	items map[string]installTicket
}{items: make(map[string]installTicket)}

func installerPath() string      { return env("INSTALLER_PATH", "dist/RemoteGate-0.1.0-2-istore.run") }
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *app) createInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		DeviceID string `json:"deviceId"`
		Token    string `json:"token"`
		Server   string `json:"server"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if _, ok := a.store.Authenticate(in.DeviceID, in.Token); !ok {
		http.Error(w, "invalid device credentials", 400)
		return
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
	t := installTicket{a, in.DeviceID, in.Token, strings.TrimRight(in.Server, "/"), time.Now().Add(15 * time.Minute)}
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
	writeJSON(w, map[string]any{"command": command, "expiresAt": t.Expires})
}
func (a *app) installerDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
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
	fmt.Fprintf(w, `#!/bin/sh
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Please run as root'; exit 1; }
command -v opkg >/dev/null || { echo 'Requires iStoreOS/OpenWrt with opkg'; exit 1; }
command -v sha256sum >/dev/null || { echo 'sha256sum is required'; exit 1; }
dir=$(mktemp -d /tmp/remotegate-setup.XXXXXX)
trap 'rm -f "$dir/package.run"; rmdir "$dir"' EXIT
echo '[1/3] Downloading RemoteGate...'
curl -fSL %s -o "$dir/package.run"
echo '%x  '"$dir/package.run" | sha256sum -c -
echo '[2/3] Installing plugin...'
sh "$dir/package.run"
echo '[3/3] Configuring connection...'
uci set remotegate.main=remotegate
uci set remotegate.main.server=%s
uci set remotegate.main.device_id=%s
uci set remotegate.main.token=%s
uci set remotegate.main.enabled=1
uci commit remotegate
chmod 600 /etc/config/remotegate
/etc/init.d/remotegate enable
/etc/init.d/remotegate restart
echo 'Installed. Check device online status in the console.'
`, shellQuote(t.Server+"/downloads/remotegate.run"), sum, shellQuote(t.Server), shellQuote(t.DeviceID), shellQuote(t.Token))
}

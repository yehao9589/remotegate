package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

type pluginUpdateView struct {
	InstalledVersion string           `json:"installedVersion"`
	Manifest         *update.Manifest `json:"manifest,omitempty"`
	Available        bool             `json:"available"`
	Online           bool             `json:"online"`
	CanUpdate        bool             `json:"canUpdate"`
	State            update.Status    `json:"state"`
	Error            string           `json:"error,omitempty"`
}

type verifiedUpdatePackage struct {
	Path                       string
	Modified, ManifestModified time.Time
	Size                       int64
	Manifest                   update.Manifest
}

func (a *app) loadUpdatePackage() (update.Manifest, error) {
	var manifest update.Manifest
	path := installerPath()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > update.MaxPackageSize {
		return manifest, errors.New("服务端未部署可用的插件安装包")
	}
	manifestInfo, err := os.Stat(path + ".json")
	if err != nil || manifestInfo.Size() > 16384 {
		return manifest, errors.New("服务端安装包缺少有效清单，请更新镜像")
	}
	a.updateMu.Lock()
	cached := a.updatePackage
	a.updateMu.Unlock()
	if cached != nil && cached.Path == path && cached.Size == info.Size() && cached.Modified.Equal(info.ModTime()) && cached.ManifestModified.Equal(manifestInfo.ModTime()) {
		return cached.Manifest, nil
	}
	raw, err := os.ReadFile(path + ".json")
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return manifest, errors.New("服务端安装包清单无效")
	}
	manifest.Size = info.Size()
	if err = manifest.Validate(); err != nil {
		return manifest, err
	}
	if manifest.AgentVersion != buildinfo.Version() {
		return manifest, errors.New("安装包与服务端版本不匹配，请使用镜像内的配套安装包")
	}
	f, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return manifest, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return manifest, errors.New("服务端安装包校验失败，已停止更新")
	}
	a.updateMu.Lock()
	a.updatePackage = &verifiedUpdatePackage{Path: path, Modified: info.ModTime(), ManifestModified: manifestInfo.ModTime(), Size: info.Size(), Manifest: manifest}
	a.updateMu.Unlock()
	return manifest, nil
}

func (a *app) agentUpdateResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := a.store.Authenticate(r.Header.Get("X-RemoteGate-Device-ID"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "invalid device credentials", 401)
		return
	}
	manifest, err := a.loadUpdatePackage()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if r.URL.Path == "/api/agent/update" {
		writeJSON(w, manifest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="RemoteGate-update.run"`)
	http.ServeFile(w, r, installerPath())
}

func (a *app) pluginUpdateView(id string) (pluginUpdateView, bool) {
	view := pluginUpdateView{State: update.Status{Phase: "idle"}}
	found := false
	for _, device := range a.store.Snapshot().Devices {
		if device.ID == id {
			view.InstalledVersion = device.PackageVersion
			found = true
			break
		}
	}
	if !found {
		return view, false
	}
	status, online := a.hub.Statuses()[id]
	view.Online, view.CanUpdate = online, online && status.CanUpdate
	a.updateMu.Lock()
	previous := a.deviceUpdates[id]
	a.updateMu.Unlock()
	if previous.State.ID != "" {
		view.State = previous.State
	}
	if previous.State.Active() && online && view.InstalledVersion == previous.State.ToVersion {
		view.State.Phase = "succeeded"
		view.State.Message = "插件更新完成，设备已重新连接"
	} else if !previous.State.Active() && (view.State.Phase == "checking" || view.State.Phase == "downloading" || view.State.Phase == "installing" || view.State.Phase == "restarting") {
		view.State.Phase = "failed"
		view.State.Message = "等待设备更新超时，请在路由器页面检查更新状态"
	}
	manifest, err := a.loadUpdatePackage()
	if err != nil {
		view.Error = err.Error()
		return view, true
	}
	view.Manifest = &manifest
	if view.InstalledVersion != "" {
		comparison, err := update.Compare(manifest.PackageVersion, view.InstalledVersion)
		view.Available = err == nil && comparison > 0
	} else {
		view.Available = true
	}
	return view, true
}

func (a *app) rememberPluginUpdate(id string, view pluginUpdateView) pluginUpdateView {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if a.deviceUpdates == nil {
		a.deviceUpdates = make(map[string]pluginUpdateView)
	}
	previous := a.deviceUpdates[id]
	if previous.State.Active() && (view.State.ID == "" || (previous.State.ID == view.State.ID && view.State.UpdatedAt.Before(previous.State.UpdatedAt))) {
		view.State = previous.State
	}
	a.deviceUpdates[id] = view
	return view
}

func (a *app) devicePluginUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	view, exists := a.pluginUpdateView(id)
	if !exists {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && !view.CanUpdate {
		writeJSON(w, view)
		return
	}
	if r.Method == http.MethodPost {
		if !view.Online {
			http.Error(w, "设备离线，请先恢复连接", 409)
			return
		}
		if !view.CanUpdate {
			http.Error(w, "旧版插件尚不支持远程更新，请先安装一次带在线更新功能的新版", 409)
			return
		}
		if view.Manifest == nil {
			http.Error(w, view.Error, 503)
			return
		}
		var requested update.Manifest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&requested) != nil || requested.PackageVersion != view.Manifest.PackageVersion || requested.SHA256 != view.Manifest.SHA256 {
			http.Error(w, "安装包已变化，请重新检查更新", 409)
			return
		}
		if !view.Available {
			http.Error(w, "当前插件已是此服务器提供的版本，或版本更高", 409)
			return
		}
		if view.State.Active() {
			http.Error(w, "已有插件更新正在进行", 409)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	message := protocol.Message{Type: "update-status", ID: makeID()}
	if r.Method == http.MethodPost {
		message.Type = "update-start"
		message.Deadline = time.Now().Add(18 * time.Second).UnixMilli()
		message.Body, _ = json.Marshal(view.Manifest)
	}
	response, err := a.hub.Request(ctx, id, message)
	if err != nil || response.Error != "" {
		message := "设备暂时无法响应，请稍后复检"
		if response.Error != "" {
			message = response.Error
		}
		if r.Method == http.MethodPost {
			http.Error(w, message, 502)
			return
		}
		view.Error = message
		writeJSON(w, view)
		return
	}
	var routerView pluginUpdateView
	if len(response.Body) > 16384 || json.Unmarshal(response.Body, &routerView) != nil {
		http.Error(w, "设备返回的更新状态无效", 502)
		return
	}
	view.InstalledVersion = routerView.InstalledVersion
	// A reboot clears /var/run. The new version reconnecting still confirms a
	// completed update even if the router's temporary status file was cleared.
	if routerView.State.Phase != "idle" || (!view.State.Active() && view.State.Phase != "succeeded") {
		view.State = routerView.State
	}
	if view.Manifest != nil {
		comparison, err := update.Compare(view.Manifest.PackageVersion, view.InstalledVersion)
		view.Available = err == nil && comparison > 0
	}
	view = a.rememberPluginUpdate(id, view)
	writeJSON(w, view)
}

type bootstrapUpdateTicket struct {
	Owner            *app
	DeviceID, Server string
	Manifest         update.Manifest
	Expires          time.Time
	ScriptUsed       bool
}

var bootstrapUpdates = struct {
	sync.Mutex
	Items map[string]bootstrapUpdateTicket
}{Items: make(map[string]bootstrapUpdateTicket)}

func (a *app) bootstrapPluginUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	view, exists := a.pluginUpdateView(id)
	if !exists {
		http.NotFound(w, r)
		return
	}
	if view.Manifest == nil {
		http.Error(w, view.Error, 503)
		return
	}
	if !view.Available {
		http.Error(w, "此服务器未提供更高版本的插件", 409)
		return
	}
	var input struct {
		Server string `json:"server"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	u, err := parsePublicURL(input.Server)
	if err != nil {
		http.Error(w, "请输入路由器可以访问的服务端 HTTP/HTTPS 地址", 400)
		return
	}
	keyBytes := make([]byte, 32)
	if _, err = rand.Read(keyBytes); err != nil {
		http.Error(w, "random source unavailable", 500)
		return
	}
	key := hex.EncodeToString(keyBytes)
	ticket := bootstrapUpdateTicket{Owner: a, DeviceID: id, Server: strings.TrimRight(u.String(), "/"), Manifest: *view.Manifest, Expires: time.Now().Add(15 * time.Minute)}
	bootstrapUpdates.Lock()
	for key, value := range bootstrapUpdates.Items {
		if time.Now().After(value.Expires) {
			delete(bootstrapUpdates.Items, key)
		}
	}
	bootstrapUpdates.Items[key] = ticket
	bootstrapUpdates.Unlock()
	command := "umask 077; f=$(mktemp /tmp/remotegate-upgrade.XXXXXX) && { curl -fSL " + shellQuote(ticket.Server+"/updates/"+key) + " -o \"$f\" && sh \"$f\"; }; rm -f \"$f\""
	writeJSON(w, map[string]any{"command": command, "expiresAt": ticket.Expires, "packageVersion": ticket.Manifest.PackageVersion})
}

func (a *app) bootstrapUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	key, resource, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/updates/"), "/")
	if resource != "" && resource != "package" {
		http.NotFound(w, r)
		return
	}
	bootstrapUpdates.Lock()
	ticket, ok := bootstrapUpdates.Items[key]
	if ok && ticket.Owner == a && time.Now().Before(ticket.Expires) {
		if resource == "" {
			if ticket.ScriptUsed {
				ok = false
			} else {
				ticket.ScriptUsed = true
				bootstrapUpdates.Items[key] = ticket
			}
		} else if ticket.ScriptUsed {
			delete(bootstrapUpdates.Items, key)
		} else {
			ok = false
		}
	}
	bootstrapUpdates.Unlock()
	if !ok || ticket.Owner != a || time.Now().After(ticket.Expires) {
		http.Error(w, "升级链接已失效，请在后台重新生成", 410)
		return
	}
	if _, exists := a.pluginUpdateView(ticket.DeviceID); !exists {
		http.Error(w, "device removed", 410)
		return
	}
	manifest, err := a.loadUpdatePackage()
	if err != nil || manifest.SHA256 != ticket.Manifest.SHA256 {
		http.Error(w, "安装包已变化，请重新生成升级命令", 410)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if resource == "package" {
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, installerPath())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `#!/bin/sh
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Please run as root'; exit 1; }
command -v opkg >/dev/null || { echo 'Requires OpenWrt with opkg'; exit 1; }
command -v sha256sum >/dev/null || { echo 'sha256sum is required'; exit 1; }
[ -f /etc/config/remotegate ] || { echo 'Existing RemoteGate configuration is required'; exit 1; }
mkdir -p /var/run/remotegate-update
mkdir /var/run/remotegate-update/lock || { echo 'Another update is running'; exit 1; }
dir=$(mktemp -d /tmp/remotegate-upgrade.XXXXXX) || { rmdir /var/run/remotegate-update/lock; exit 1; }
was_enabled=0
/etc/init.d/remotegate enabled && was_enabled=1
installed=0
cleanup() {
 code=$?
 set +e
 if [ "$installed" = 1 ]; then
  cp "$dir/configuration.backup" /etc/config/remotegate || code=1
  chmod 600 /etc/config/remotegate || code=1
  if [ "$was_enabled" = 1 ]; then /etc/init.d/remotegate enable; else /etc/init.d/remotegate disable; fi
  /etc/init.d/remotegate restart || code=1
 fi
 rm -f "$dir/package.run" "$dir/configuration.backup"
 rmdir "$dir" /var/run/remotegate-update/lock
 exit "$code"
}
trap cleanup EXIT
cp /etc/config/remotegate "$dir/configuration.backup"
curl -fSL %s -o "$dir/package.run"
echo '%s  '"$dir/package.run" | sha256sum -c -
installed=1
sh "$dir/package.run" --update
echo 'Plugin upgraded. Existing device and connection configuration preserved.'
`, shellQuote(ticket.Server+"/updates/"+key+"/package"), ticket.Manifest.SHA256)
}

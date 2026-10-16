package main

import (
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
	Locked           bool             `json:"locked"`
	CanManageUpdate  bool             `json:"canManageUpdate"`
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
	deviceID := r.Header.Get("X-RemoteGate-Device-ID")
	if _, ok := a.store.Authenticate(deviceID, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "invalid device credentials", 401)
		return
	}
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, "安装与更新配置不可用", 503)
		return
	}
	if p.locked(deviceID) {
		http.Error(w, "此设备已锁定版本，请在安装与更新中心解除锁定", 409)
		return
	}
	if r.URL.Query().Get("grant") != "" || r.URL.Query().Get("artifact") != "" {
		item, rollback, err := a.grantedArtifact(p, deviceID, r.URL.Query().Get("artifact"), r.URL.Query().Get("grant"))
		if err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		if r.URL.Path == "/api/agent/update" {
			m := item.Manifest
			m.Artifact, m.Grant, m.Rollback = m.SHA256, r.URL.Query().Get("grant"), rollback
			writeJSON(w, m)
		} else {
			http.ServeFile(w, r, item.Path)
		}
		return
	}
	if r.URL.Query().Get("tools") == "1" {
		arch := update.Architecture(r.URL.Query().Get("arch"))
		if arch == "" {
			http.Error(w, "不支持此 CPU 架构", 400)
			return
		}
		item, err := a.packageArtifact(p, "", arch)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		if r.URL.Path == "/api/agent/update" {
			writeJSON(w, item.Manifest)
		} else {
			http.ServeFile(w, r, item.Path)
		}
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
	view.CanManageUpdate = online && status.CanManageUpdate
	p, centerErr := a.packageCenterStore()
	if centerErr == nil {
		view.Locked = p.locked(id)
	}
	a.updateMu.Lock()
	previous := a.deviceUpdates[id]
	a.updateMu.Unlock()
	if previous.State.ID != "" {
		view.State = previous.State
	} else if centerErr == nil {
		view.State = p.latestUpdateState(id)
	}
	if view.State.Active() && online && status.Since.After(view.State.StartedAt) && view.InstalledVersion == view.State.ToVersion {
		view.State.Phase = "succeeded"
		view.State.Message = "插件更新完成，设备已重新连接"
	} else if !view.State.Active() && (view.State.Phase == "checking" || view.State.Phase == "downloading" || view.State.Phase == "installing" || view.State.Phase == "restarting") {
		view.State.Phase = "failed"
		view.State.Message = "等待设备更新超时，请在路由器页面检查更新状态"
	}
	if previous.State.ID != "" && view.State.Phase != previous.State.Phase {
		a.updateMu.Lock()
		cached := a.deviceUpdates[id]
		if cached.State.ID == previous.State.ID {
			cached.State = view.State
			a.deviceUpdates[id] = cached
		}
		a.updateMu.Unlock()
		if centerErr == nil {
			p.rememberUpdateState(id, view.State)
		}
	}
	manifest, err := a.loadUpdatePackage()
	if err != nil {
		view.Error = err.Error()
		return view, true
	}
	view.Manifest = &manifest
	if view.CanManageUpdate && centerErr == nil {
		for _, device := range a.store.Snapshot().Devices {
			if device.ID == id && update.Architecture(device.Info.Arch) != "" {
				if item, err := a.packageArtifact(p, "", update.Architecture(device.Info.Arch)); err == nil {
					view.Manifest = &item.Manifest
				}
			}
		}
	}
	if view.InstalledVersion != "" {
		comparison, err := update.Compare(manifest.PackageVersion, view.InstalledVersion)
		view.Available = err == nil && comparison > 0
	} else {
		view.Available = true
	}
	view.Available = view.Available && !view.Locked
	return view, true
}

func (a *app) rememberPluginUpdate(id string, view pluginUpdateView) pluginUpdateView {
	a.updateMu.Lock()
	if a.deviceUpdates == nil {
		a.deviceUpdates = make(map[string]pluginUpdateView)
	}
	previous := a.deviceUpdates[id]
	if previous.State.Active() && (view.State.ID == "" || (previous.State.ID == view.State.ID && view.State.UpdatedAt.Before(previous.State.UpdatedAt))) {
		view.State = previous.State
	}
	a.deviceUpdates[id] = view
	a.updateMu.Unlock()
	if p, err := a.packageCenterStore(); err == nil {
		p.rememberUpdateState(id, view.State)
	}
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
	if r.Method == http.MethodGet {
		view, err := a.refreshPackageUpdate(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, view)
		return
	}
	if !view.Online || !view.CanUpdate || view.Locked || !view.Available || view.State.Active() {
		http.Error(w, "设备离线、版本锁定、正在更新或没有可用更新", 409)
		return
	}
	if view.Manifest == nil {
		http.Error(w, view.Error, 503)
		return
	}
	var requested update.Manifest
	if decodePackageInput(w, r, &requested) != nil || requested.PackageVersion != view.Manifest.PackageVersion || requested.SHA256 != view.Manifest.SHA256 || requested.Rollback || requested.Grant != "" {
		http.Error(w, "安装包已变化，请重新检查更新", 409)
		return
	}
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, "安装配置不可用", 503)
		return
	}
	arch := ""
	if view.CanManageUpdate {
		for _, d := range a.store.Snapshot().Devices {
			if d.ID == id {
				arch = update.Architecture(d.Info.Arch)
			}
		}
	}
	item, err := a.packageArtifact(p, "", arch)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	view, err = a.startPackageUpdate(r.Context(), id, item, false)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
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
	if view.Locked {
		http.Error(w, "此设备已锁定版本，请先解除锁定", 409)
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

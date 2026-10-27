package main

import (
	"fmt"
	"github.com/local/remotegate/internal/routercompat"
	"net/http"
	"strings"
	"time"
)

var installPhases = map[string]string{"preflight": "检查系统与安装条件", "downloading": "下载匹配架构的插件", "verifying": "核对安装包完整性", "installing": "安装路由器插件", "configuring": "应用安装模板", "starting": "启动客户端", "waiting": "等待设备连接并核对版本", "failed": "安装未完成，请检查路由器终端输出"}
var installChecks = map[string]string{"root": "root 权限", "opkg": "opkg 安装工具", "arch": "CPU 架构", "curl": "下载与校验工具", "space": "可用空间", "server": "服务器连接", "existing": "已有连接配置", "download": "下载安装包", "hash": "安装包校验", "install": "插件安装", "configuration": "连接配置", "start": "启动客户端"}

func (a *app) installReportHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, "安装记录不可用", 503)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := r.Header.Get("X-RemoteGate-Install-Key")
	grant, exists := p.installs[key]
	if !exists || time.Now().After(grant.Expires) {
		http.Error(w, "安装状态授权已过期", 403)
		return
	}
	var input struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if decodePackageInput(w, r, &input) != nil || installPhases[input.Phase] == "" || (input.Reason != "" && installChecks[input.Reason] == "") {
		http.Error(w, "状态参数无效", 400)
		return
	}
	for i, op := range p.state.History {
		if op.ID != grant.Operation || op.DeviceID != grant.Device {
			continue
		}
		if op.State.Phase == "succeeded" || op.State.Phase == "failed" {
			http.Error(w, "安装记录已结束", 409)
			return
		}
		rank := map[string]int{"ready": 0, "preflight": 1, "downloading": 2, "verifying": 3, "installing": 4, "configuring": 5, "starting": 6, "waiting": 7, "failed": 8}
		if rank[input.Phase] < rank[op.State.Phase] {
			http.Error(w, "过期安装状态", 409)
			return
		}
		state := op.State
		state.Phase, state.Message = input.Phase, installPhases[input.Phase]
		if input.Reason != "" {
			if input.Phase == "failed" {
				state.Message = "未完成：" + installChecks[input.Reason]
			} else {
				state.Message = "检查通过：" + installChecks[input.Reason]
			}
		}
		p.progressLocked(i, state)
		if err = p.saveLocked(); err != nil {
			http.Error(w, "状态保存失败", 500)
			return
		}
		w.WriteHeader(204)
		return
	}
	http.NotFound(w, r)
}
func installScriptText(t installTicket, sum string, size int) string {
	var choices strings.Builder
	choices.WriteString(routercompat.ArchitectureShell + "\narch=$(remotegate_arch) || exit 1\n")
	fmt.Fprintf(&choices, "pkg_url=%s\npkg_sha=%s\npkg_size=%d\ncase \"$arch\" in\n", shellQuote(t.Server+"/downloads/remotegate.run"), shellQuote(sum), size)
	for _, choice := range []struct{ uname, arch string }{{"amd64", "amd64"}, {"arm64", "arm64"}, {"armv7", "armv7"}} {
		fmt.Fprintf(&choices, " %s)\n", choice.uname)
		if item, exists := t.Packages[choice.arch]; exists {
			fmt.Fprintf(&choices, "  pkg_url=%s; pkg_sha=%s; pkg_size=%d\n", shellQuote(t.Server+"/downloads/remotegate.run?artifact="+item.Manifest.SHA256), shellQuote(item.Manifest.SHA256), item.Manifest.Size)
		}
		choices.WriteString("  ;;\n")
	}
	choices.WriteString(" *) echo 'Unsupported CPU architecture'; exit 1;;\nesac\n")
	remote, autostart := "1", "enable"
	if !t.Profile.RemoteAccess {
		remote = "0"
	}
	if !t.Profile.AutoStart {
		autostart = "disable"
	}
	return strings.NewReplacer("__REPORT_KEY__", shellQuote(t.ReportKey), "__REPORT_URL__", shellQuote(t.Server+"/api/install/report"), "__CHOICES__", choices.String(), "__SERVER__", shellQuote(t.Server), "__DEVICE__", shellQuote(t.DeviceID), "__TOKEN__", shellQuote(t.Token), "__REMOTE__", remote, "__AUTOSTART__", autostart, "__INTERFACE_MODE__", shellQuote(t.Profile.InterfaceMode)).Replace(`#!/bin/sh
set -eu
umask 077
report_key=__REPORT_KEY__
report() { curl -fsS --connect-timeout 2 --max-time 4 -X POST -H "X-RemoteGate-Install-Key: $report_key" -H 'Content-Type: application/json' -d "{\"phase\":\"$1\",\"reason\":\"${2:-}\"}" __REPORT_URL__ >/dev/null 2>&1 || true; }
setupdir=''
completed=0
attempted=0
fail_reason=root
cleanup() {
 code=$?
 set +e
 if [ "$completed" != 1 ]; then
  report failed "$fail_reason"
  if [ "$attempted" = 1 ] && [ -f "$setupdir/configuration.backup" ]; then
   cp "$setupdir/configuration.backup" /etc/config/remotegate
   chmod 600 /etc/config/remotegate
   /etc/init.d/remotegate restart
  fi
 fi
 if [ -n "$setupdir" ]; then rm -f "$setupdir/package.run" "$setupdir/configuration.backup"; rmdir "$setupdir"; fi
 exit "$code"
}
trap cleanup EXIT
echo '[1/7] Checking installation requirements...'
report preflight
[ "$(id -u)" = 0 ] || { echo 'Please run as root'; exit 1; }
report preflight root
fail_reason=opkg
command -v opkg >/dev/null || { echo 'Requires an opkg-based OpenWrt-family firmware with LuCI; APK packages are not provided by this build'; exit 1; }
report preflight opkg
fail_reason=curl
command -v curl >/dev/null && command -v sha256sum >/dev/null || { echo 'curl and sha256sum are required'; exit 1; }
report preflight curl
fail_reason=arch
__CHOICES__
report preflight arch
fail_reason=existing
existing_device=$(uci -q get remotegate.main.device_id || true)
[ -z "$existing_device" ] || [ "$existing_device" = __DEVICE__ ] || { echo 'Router already has a RemoteGate device; use update to preserve its identity.'; exit 1; }
report preflight existing
fail_reason=space
needed=$((pkg_size / 1024 * 4 + 16384))
for storage in /tmp /etc/config; do
 free=$(df -Pk "$storage" | awk 'NR==2 {print $4}')
 [ -n "$free" ] && [ "$free" -ge "$needed" ] || { echo "Not enough free space in $storage"; exit 1; }
done
report preflight space
fail_reason=server
curl -fIsS --connect-timeout 10 --max-time 15 "$pkg_url" >/dev/null
report preflight server
setupdir=$(mktemp -d /tmp/remotegate-setup.XXXXXX)
if [ -f /etc/config/remotegate ]; then cp /etc/config/remotegate "$setupdir/configuration.backup"; fi
echo '[2/7] Downloading matching architecture package...'
fail_reason=download
report downloading
curl -fSL --retry 2 --retry-delay 1 --connect-timeout 15 --max-time 180 "$pkg_url" -o "$setupdir/package.run"
echo '[3/7] Verifying package...'
fail_reason=hash
report verifying
echo "$pkg_sha  $setupdir/package.run" | sha256sum -c -
echo '[4/7] Installing plugin...'
fail_reason=install
report installing
attempted=1
sh "$setupdir/package.run"
echo '[5/7] Applying installation template...'
fail_reason=configuration
report configuring
out_interface=''
if [ __INTERFACE_MODE__ = select ]; then
 echo 'Choose an outbound interface (0 = automatic):'
 echo '0) Automatic'
 i=0
 for nic_path in /sys/class/net/*; do [ -d "$nic_path" ] || continue; i=$((i+1)); echo "$i) ${nic_path##*/}"; done
 printf 'Selection [0]: '
 read selection || selection=0
 case "${selection:-0}" in *[!0-9]*) echo 'Invalid selection'; exit 1;; esac
 i=0
 for nic_path in /sys/class/net/*; do [ -d "$nic_path" ] || continue; i=$((i+1)); if [ "$i" = "${selection:-0}" ]; then out_interface=${nic_path##*/}; fi; done
 [ "${selection:-0}" = 0 ] || [ -n "$out_interface" ] || { echo 'Interface not found'; exit 1; }
fi
uci set remotegate.main=remotegate
uci set remotegate.main.server=__SERVER__
uci set remotegate.main.device_id=__DEVICE__
uci set remotegate.main.token=__TOKEN__
uci set remotegate.main.remote_access=__REMOTE__
uci set remotegate.main.interface="$out_interface"
uci set remotegate.main.enabled=1
uci commit remotegate
chmod 600 /etc/config/remotegate
echo '[6/7] Starting client...'
fail_reason=start
report starting
/etc/init.d/remotegate __AUTOSTART__
if [ -z "$existing_device" ]; then /etc/init.d/remotegate start; else /etc/init.d/remotegate restart; fi
completed=1
echo '[7/7] Waiting for authenticated device connection; check the console...'
report waiting
`)
}

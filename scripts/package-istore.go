//go:build ignore

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/local/remotegate/internal/buildinfo"
	"os"
	"path/filepath"
	"strings"
)

type entry struct {
	name string
	data []byte
	mode int64
}

func archive(entries []entry) []byte {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	t := tar.NewWriter(g)
	dirs := make(map[string]bool)
	for _, e := range entries {
		parts := strings.Split(e.name, "/")
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/") + "/"
			if !dirs[dir] {
				if err := t.WriteHeader(&tar.Header{Name: dir, Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
					panic(err)
				}
				dirs[dir] = true
			}
		}
		if err := t.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			panic(err)
		}
		if _, err := t.Write(e.data); err != nil {
			panic(err)
		}
	}
	t.Close()
	g.Close()
	return b.Bytes()
}
func text(n, s string, m int64) entry { return entry{n, []byte(s), m} }
func source(n, path string, m int64) entry {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return entry{n, b, m}
}
func main() {
	routeGuard, err := os.ReadFile("deploy/openwrt/remotegate-route-guard")
	if err != nil {
		panic(err)
	}
	files := []entry{
		text("etc/config/remotegate", `config remotegate 'main'
 option enabled '0'
 option server ''
 option device_id ''
 option token ''
 option interface ''
`, 0600),
		source("usr/lib/lua/luci/controller/remotegate.lua", "deploy/openwrt/luci/controller.lua", 0644),
		source("usr/lib/lua/luci/model/cbi/remotegate.lua", "deploy/openwrt/luci/model.lua", 0644),
		source("usr/lib/lua/luci/view/remotegate/map.htm", "deploy/openwrt/luci/map.htm", 0644),
		source("usr/lib/lua/luci/view/remotegate/style.htm", "deploy/openwrt/luci/page.css", 0644),
		source("usr/lib/lua/luci/view/remotegate/script.htm", "deploy/openwrt/luci/page.js", 0644),
		text("usr/share/remotegate/version", buildinfo.PackageVersion()+"\n", 0644),
		text("usr/libexec/remotegate-config", `#!/usr/bin/lua
local u = require("uci").cursor()
local json = require "luci.jsonc"
local c = u:get_all("remotegate", "main") or {}
if c.enabled ~= "1" then os.exit(1) end
if not c.server or c.server == "" or not c.device_id or c.device_id == "" or not c.token or c.token == "" then os.exit(1) end
io.write(json.stringify({serverUrl=c.server,deviceId=c.device_id,token=c.token,interface=c.interface or "",mark=51820,allowPublicTargets=false}))
`, 0755),
		entry{"usr/libexec/remotegate-route-guard", routeGuard, 0755},
		text("etc/init.d/remotegate", `#!/bin/sh /etc/rc.common
USE_PROCD=1
START=98
STOP=10
start_service() {
 umask 077
 mkdir -p /var/run/remotegate
 /usr/libexec/remotegate-config > /var/run/remotegate/agent.json || return 0
 /usr/libexec/remotegate-route-guard start
 procd_open_instance
 procd_set_param command /usr/sbin/remotegate-agent /var/run/remotegate/agent.json
 procd_set_param respawn 5 30 0
 procd_set_param stdout 1
 procd_set_param stderr 1
 procd_close_instance
}
stop_service() { /usr/libexec/remotegate-route-guard stop; }
service_triggers() { procd_add_reload_trigger remotegate firewall openclash network; }
reload_service() { /usr/libexec/remotegate-route-guard refresh; stop; start; }
`, 0755),
	}
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		b, err := os.ReadFile("dist/remotegate-agent-linux-" + arch)
		if err != nil {
			panic(err)
		}
		data := archive(append(append([]entry{}, files...), entry{"usr/sbin/remotegate-agent", b, 0755}))
		control := archive([]entry{text("control", fmt.Sprintf("Package: luci-app-remotegate\nVersion: %s\nArchitecture: all\nMaintainer: RemoteGate\nSection: net\nPriority: optional\nDepends: luci-base, luci-compat\nDescription: RemoteGate agent and LuCI configuration (%s)\n", buildinfo.PackageVersion(), arch), 0644), text("conffiles", "/etc/config/remotegate\n", 0644), text("postinst", `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
chmod 600 /etc/config/remotegate
rm -f /tmp/luci-indexcache
/etc/init.d/remotegate enable
echo 'Installed. Open Services -> RemoteGate, enter credentials, then Save & Apply.'
exit 0
`, 0755), text("prerm", `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/remotegate stop
/etc/init.d/remotegate disable
exit 0
`, 0755)})
		ipk := archive([]entry{text("debian-binary", "2.0\n", 0644), entry{"control.tar.gz", control, 0644}, entry{"data.tar.gz", data, 0644}})
		os.MkdirAll("dist/istore", 0755)
		if err := os.WriteFile(filepath.Join("dist/istore", "remotegate-"+arch+".ipk"), ipk, 0644); err != nil {
			panic(err)
		}
	}
	var payload []entry
	for _, a := range []string{"amd64", "arm64", "armv7"} {
		b, _ := os.ReadFile("dist/istore/remotegate-" + a + ".ipk")
		payload = append(payload, entry{"remotegate-" + a + ".ipk", b, 0644})
	}
	header := `#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Root required'; exit 1; }
command -v opkg >/dev/null || { echo 'This build requires opkg (iStoreOS 21.02/22.03/24.10).'; exit 1; }
case "$(uname -m)" in
 x86_64) arch=amd64;;
 aarch64) arch=arm64;;
 armv7l) arch=armv7;;
 *) echo 'Unsupported CPU architecture'; exit 1;;
esac
dir=$(mktemp -d /tmp/remotegate-install.XXXXXX)
trap 'rm -f "$dir"/*.ipk; rmdir "$dir"' EXIT
line=$(awk '/^__PAYLOAD__$/ {print NR+1; exit}' "$0")
tail -n +"$line" "$0" | tar -xz -C "$dir"
opkg install "$dir/remotegate-$arch.ipk"
echo 'Refresh LuCI, then open Services -> RemoteGate.'
exit 0
__PAYLOAD__
`
	out := append([]byte(header), archive(payload)...)
	output := filepath.Join("dist", buildinfo.PackageFilename())
	if err := os.WriteFile(output, out, 0755); err != nil {
		panic(err)
	}
	manifest := map[string]any{"sha256": fmt.Sprintf("%x", sha256.Sum256(out)), "packageVersion": buildinfo.PackageVersion(), "agentVersion": buildinfo.Version(), "release": buildinfo.Current(), "packageName": "luci-app-remotegate", "architectures": []string{"x86_64", "ARM64", "ARMv7"}, "contents": []string{"RemoteGate 客户端", "新版 LuCI 路由器面板", "状态刷新与接入引导", "版本上报", "开机启动服务", "连接配置生成器", "直连路由保护脚本"}}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(output+".json", raw, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("Created %s (%d bytes)\n", output, len(out))
}

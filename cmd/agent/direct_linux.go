//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Dedicated to RemoteGate sockets; never reuse WireGuard's common 0xca6c mark.
const directMark = 0x52470001
const directTable = "138039"
const directProtocol = "242"
const directPriority = "1"

type routeCommand func(context.Context, string, ...string) (string, error)
type wanRoute struct {
	Interface, Gateway string
	Metric             int
}
type directWANDialer struct {
	mu     sync.Mutex
	manual string
	run    routeCommand
	guard  string
	lock   func(context.Context) (func(), error)
}
type directWANConn struct {
	net.Conn
	iface string
}

func (c *directWANConn) EgressInterface() string { return c.iface }

func routeExec(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
func newDirectWANDialer(cfg Config) *directWANDialer {
	return &directWANDialer{manual: cfg.Interface, run: routeExec, lock: directRouteLock, guard: "/usr/libexec/remotegate-route-guard"}
}

func validDevice(name string) bool {
	if name == "" || len(name) > 15 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.:-", c)) {
			return false
		}
	}
	return name != "lo"
}
func virtualUplink(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"tun", "tap", "utun", "wg", "tailscale", "mihomo", "clash", "docker", "veth", "vpn", "zt"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
func routeField(fields []string, key string) string {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == key {
			return fields[i+1]
		}
	}
	return ""
}
func parseDefaultRoutes(raw, manual string) []wanRoute {
	var out []wanRoute
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "default" {
			continue
		}
		device := routeField(f, "dev")
		if !validDevice(device) || manual != "" && manual != device || manual == "" && virtualUplink(device) {
			continue
		}
		gateway := routeField(f, "via")
		if gateway != "" && net.ParseIP(gateway) == nil {
			continue
		}
		if ip := net.ParseIP(gateway); ip != nil && ip.IsUnspecified() {
			gateway = ""
		}
		metric, _ := strconv.Atoi(routeField(f, "metric"))
		out = append(out, wanRoute{Interface: device, Gateway: gateway, Metric: metric})
	}
	return out
}

func discoverWAN(ctx context.Context, run routeCommand, manual string) (map[string]wanRoute, error) {
	if manual != "" && !validDevice(manual) {
		return nil, errors.New("指定的出口网卡无效")
	}
	var dump struct {
		Interfaces []struct {
			Name   string `json:"interface"`
			Up     bool   `json:"up"`
			Device string `json:"l3_device"`
			Proto  string `json:"proto"`
			Routes []struct {
				Target  string `json:"target"`
				Mask    int    `json:"mask"`
				Gateway string `json:"nexthop"`
				Metric  int    `json:"metric"`
			} `json:"route"`
		} `json:"interface"`
	}
	selected := map[string]wanRoute{}
	priority := map[string]int{}
	configuredWAN := false
	if raw, err := run(ctx, "ubus", "call", "network.interface", "dump"); err == nil && json.Unmarshal([]byte(raw), &dump) == nil {
		for _, iface := range dump.Interfaces {
			wan := iface.Name == "wan" || iface.Name == "wan6"
			configuredWAN = configuredWAN || wan
			if !iface.Up || !validDevice(iface.Device) || manual != "" && manual != iface.Device {
				continue
			}
			if manual == "" && (virtualUplink(iface.Device) || iface.Proto == "wireguard" || iface.Proto == "openvpn" || iface.Proto == "pptp") {
				continue
			}
			for _, r := range iface.Routes {
				if r.Mask != 0 || r.Target != "0.0.0.0" && r.Target != "::" || r.Gateway != "" && net.ParseIP(r.Gateway) == nil {
					continue
				}
				family := "-4"
				if r.Target == "::" {
					family = "-6"
				}
				p := 1
				if wan {
					p = 2
				}
				old, exists := selected[family]
				if !exists || p > priority[family] || p == priority[family] && r.Metric < old.Metric {
					gateway := r.Gateway
					if ip := net.ParseIP(gateway); ip != nil && ip.IsUnspecified() {
						gateway = ""
					}
					selected[family] = wanRoute{Interface: iface.Device, Gateway: gateway, Metric: r.Metric}
					priority[family] = p
				}
			}
		}
	}
	// A configured but down WAN must not silently fall back to a VPN or LAN.
	// Bypass-router installations without wan/wan6 can use a physical main default.
	for _, family := range []string{"-4", "-6"} {
		if configuredWAN && manual == "" && priority[family] < 2 {
			delete(selected, family)
		}
		if _, ok := selected[family]; ok {
			continue
		}
		if configuredWAN && manual == "" {
			continue
		}
		raw, err := run(ctx, "ip", family, "route", "show", "table", "main")
		if err != nil {
			continue
		}
		for _, candidate := range parseDefaultRoutes(raw, manual) {
			if old, ok := selected[family]; !ok || candidate.Metric < old.Metric {
				selected[family] = candidate
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("没有可用的 WAN 直连出口；已停止连接，不回退到代理网络")
	}
	return selected, nil
}

func (d *directWANDialer) configureFamily(ctx context.Context, family string, route wanRoute) error {
	run := func(args ...string) (string, error) { return d.run(ctx, "ip", append([]string{family}, args...)...) }
	rules, err := run("-N", "rule", "show")
	if err != nil {
		return err
	}
	mark := fmt.Sprintf("0x%x", directMark)
	installed := false
	for _, line := range strings.Split(rules, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.TrimSuffix(f[0], ":") != directPriority {
			continue
		}
		if (routeField(f, "fwmark") == mark || routeField(f, "fwmark") == mark+"/0xffffffff") && routeField(f, "lookup") == directTable {
			installed = true
			continue
		}
		return errors.New("直连路由优先级 1 已被其他规则使用；未修改原规则")
	}
	existing, err := run("-N", "route", "show", "table", directTable)
	if err != nil {
		// Older OpenWrt kernels/iproute2 report only "Dump terminated" for
		// an absent table. Verify against a full successful dump instead of
		// relying on the error wording or treating arbitrary failures as empty.
		all, allErr := run("-N", "route", "show", "table", "all")
		if allErr != nil {
			return err
		}
		existing = ""
		for _, line := range strings.Split(all, "\n") {
			if routeField(strings.Fields(line), "table") == directTable {
				existing += line + "\n"
			}
		}
	}
	for _, line := range strings.Split(existing, "\n") {
		if strings.TrimSpace(line) != "" && routeField(strings.Fields(line), "proto") != directProtocol {
			return errors.New("直连专用路由表已被其他配置使用；未覆盖原路由")
		}
	}
	// The terminal route prevents a missing family/gateway from falling through
	// to a lower-priority TUN table. Install it before the marked policy rule.
	if _, err = run("route", "replace", "unreachable", "default", "table", directTable, "metric", "42760", "proto", directProtocol); err != nil {
		return err
	}
	if route.Interface != "" {
		args := []string{"route", "replace", "default"}
		if route.Gateway != "" {
			args = append(args, "via", route.Gateway)
		}
		args = append(args, "dev", route.Interface, "table", directTable, "metric", "10", "proto", directProtocol)
		if route.Gateway != "" {
			args = append(args, "onlink")
		}
		if _, err = run(args...); err != nil {
			return err
		}
	} else {
		// Delete only RemoteGate's formerly working default, never other routes.
		if strings.Contains(existing, "metric 10 ") || strings.HasSuffix(strings.TrimSpace(existing), "metric 10") {
			if _, err = run("route", "del", "default", "table", directTable, "metric", "10", "proto", directProtocol); err != nil {
				return err
			}
		}
	}
	if !installed {
		if _, err = run("rule", "add", "priority", directPriority, "fwmark", mark+"/0xffffffff", "lookup", directTable); err != nil {
			// Concurrent updater/agent setup may have installed the exact rule.
			now, _ := run("-N", "rule", "show")
			if !strings.Contains(now, mark) || !strings.Contains(now, "lookup "+directTable) {
				return err
			}
		}
	}
	return nil
}

func (d *directWANDialer) prepare(ctx context.Context) (map[string]wanRoute, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Recheck before every new control/update/DNS TCP connection. A cached
	// firewall check could otherwise leak a new connection during proxy reload.
	if d.lock != nil {
		unlock, err := d.lock(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	routes, err := discoverWAN(ctx, d.run, d.manual)
	if err != nil {
		return nil, err
	}
	for _, family := range []string{"-4", "-6"} {
		if err = d.configureFamily(ctx, family, routes[family]); err != nil {
			if family == "-6" && routes[family].Interface == "" {
				continue
			}
			return nil, fmt.Errorf("WAN 直连路由配置失败：%w", err)
		}
	}
	if _, err = d.run(ctx, d.guard, "refresh"); err != nil {
		return nil, fmt.Errorf("代理绕过规则配置失败：%w", err)
	}
	return routes, nil
}

// Serialize policy changes across the tunnel, update checker and supervisor.
func directRouteLock(ctx context.Context) (func(), error) {
	path := "/var/run/remotegate/direct-route.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (d *directWANDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.Split(host, "%")[0])
	if ip == nil {
		return nil, errors.New("直连拨号需要已解析的服务器地址")
	}
	if ip.IsLoopback() {
		return boundDialer("", 0).DialContext(ctx, network, address)
	}
	routes, err := d.prepare(ctx)
	if err != nil {
		return nil, err
	}
	family := "-4"
	if ip.To4() == nil {
		family = "-6"
	}
	route := routes[family]
	if route.Interface == "" {
		return nil, errors.New("服务器地址对应的 WAN 出口不可用；未回退到代理")
	}
	conn, err := boundDialer(route.Interface, directMark).DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("WAN 出口 %s 连接失败：%w", route.Interface, err)
	}
	return &directWANConn{Conn: conn, iface: route.Interface}, nil
}

func maintainDirectRoute(ctx context.Context, cfg Config) {
	d := newDirectWANDialer(cfg)
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	var previous string
	for {
		_, err := d.prepare(ctx)
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != previous {
			log.Printf("WAN direct protection: %s", message)
		}
		previous = message
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

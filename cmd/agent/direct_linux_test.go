//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWANDiscoveryPrefersNetifdWANOverTunnelAndLAN(t *testing.T) {
	raw := `{"interface":[{"interface":"lan","up":true,"l3_device":"br-lan","route":[{"target":"0.0.0.0","mask":0,"nexthop":"10.0.0.1","metric":0}]},{"interface":"wan","up":true,"proto":"pppoe","l3_device":"pppoe-wan","route":[{"target":"0.0.0.0","mask":0,"nexthop":"100.64.0.1","metric":50}]},{"interface":"wan6","up":true,"l3_device":"eth1","route":[{"target":"::","mask":0,"nexthop":"fe80::1"}]}]}`
	run := func(_ context.Context, name string, args ...string) (string, error) {
		if name != "ubus" {
			return "", nil
		}
		return raw, nil
	}
	routes, err := discoverWAN(context.Background(), run, "")
	if err != nil || routes["-4"].Interface != "pppoe-wan" || routes["-6"].Interface != "eth1" {
		t.Fatal(routes, err)
	}
	manual, err := discoverWAN(context.Background(), run, "br-lan")
	if err != nil || manual["-4"].Interface != "br-lan" {
		t.Fatal("explicit selection lost", manual, err)
	}
}

func TestWANDiscoveryFailsClosedWhenWANIsDown(t *testing.T) {
	raw := `{"interface":[{"interface":"wan","up":false,"l3_device":"eth1"},{"interface":"lan","up":true,"l3_device":"br-lan","route":[{"target":"0.0.0.0","mask":0,"nexthop":"10.0.0.1"}]}]}`
	run := func(_ context.Context, name string, args ...string) (string, error) {
		if name != "ubus" {
			t.Fatal("down WAN fell back to main route")
		}
		return raw, nil
	}
	_, err := discoverWAN(context.Background(), run, "")
	if err == nil || !strings.Contains(err.Error(), "不回退") {
		t.Fatal(err)
	}
}

func TestWANDiscoveryBypassRouterAndManualVPN(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) (string, error) {
		if name == "ubus" {
			return "", errors.New("no netifd")
		}
		if args[0] == "-6" {
			return "", nil
		}
		return "default via 198.18.0.1 dev utun0 metric 0\ndefault dev wg0 metric 1\ndefault via 192.0.2.1 dev eth1 metric 50\ndefault via 10.0.0.1 dev br-lan metric 20\n", nil
	}
	auto, err := discoverWAN(context.Background(), run, "")
	if err != nil || auto["-4"].Interface != "br-lan" {
		t.Fatal(auto, err)
	}
	manual, err := discoverWAN(context.Background(), run, "wg0")
	if err != nil || manual["-4"].Interface != "wg0" {
		t.Fatal(manual, err)
	}
	if _, err = discoverWAN(context.Background(), run, "eth1;bad"); err == nil {
		t.Fatal("invalid interface accepted")
	}
}

func TestDirectPolicyOwnsOnlyMarkedRouteAndStopsFamilyFallback(t *testing.T) {
	var commands []string
	d := &directWANDialer{run: func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		commands = append(commands, line)
		if strings.Contains(line, "rule show") {
			return "0: from all lookup local\n100: from all lookup 354\n32766: from all lookup main\n", nil
		}
		return "", nil
	}}
	if err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "pppoe-wan", Gateway: "100.64.0.1"}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(commands, "\n")
	for _, required := range []string{"replace unreachable default table 138039", "via 100.64.0.1 dev pppoe-wan table 138039 metric 10 proto 242 onlink", "rule add priority 1 fwmark 0x52470001/0xffffffff lookup 138039"} {
		if !strings.Contains(all, required) {
			t.Fatal("missing policy", required, all)
		}
	}
	if strings.Index(all, "replace unreachable") > strings.Index(all, "rule add") {
		t.Fatal("policy permits fallback during setup")
	}
	if strings.Contains(all, "flush") || strings.Contains(all, "rule del") || strings.Contains(all, "route replace default via 100.64.0.1 dev pppoe-wan table main") {
		t.Fatal("modified unrelated routes", all)
	}
	commands = nil
	if err := d.configureFamily(context.Background(), "-6", wanRoute{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(commands, "\n"), "dev ") {
		t.Fatal("missing IPv6 WAN fabricated an egress")
	}
}

func TestDirectPolicyRejectsConflictingPriorityAndForeignTable(t *testing.T) {
	for _, conflict := range []string{"priority", "table"} {
		d := &directWANDialer{run: func(_ context.Context, name string, args ...string) (string, error) {
			line := strings.Join(args, " ")
			if strings.Contains(line, "rule show") {
				if conflict == "priority" {
					return "1: from all lookup 99", nil
				}
				return "0: from all lookup local", nil
			}
			if strings.Contains(line, "route show") {
				return "default via 192.0.2.1 dev eth0 proto static", nil
			}
			return "", fmt.Errorf("unexpected mutation: %s", line)
		}}
		err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "eth1"})
		if err == nil || !strings.Contains(err.Error(), "未") {
			t.Fatal(conflict, err)
		}
	}
}

func TestDirectPolicyVerifiesAbsentTableOnOlderOpenWrt(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		mutations := 0
		d := &directWANDialer{run: func(_ context.Context, _ string, args ...string) (string, error) {
			line := strings.Join(args, " ")
			if strings.Contains(line, "rule show") {
				return "0: from all lookup local", nil
			}
			if strings.Contains(line, "route show table all") {
				if foreign {
					return "default dev eth0 table 138039 proto 4", nil
				}
				return "default dev eth1 table 254 proto 4", nil
			}
			if strings.Contains(line, "route show table 138039") {
				return "Dump terminated", errors.New("exit status 2")
			}
			mutations++
			return "", nil
		}}
		err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "eth1"})
		if foreign && (err == nil || mutations != 0) {
			t.Fatal("fallback overwrote foreign table", err, mutations)
		}
		if !foreign && (err != nil || mutations == 0) {
			t.Fatal("absent table compatibility failed", err, mutations)
		}
	}
}

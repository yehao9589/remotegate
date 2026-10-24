//go:build linux

package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// scripts/test-direct-routing.sh runs this inside an isolated router namespace.
// No production routes, firewall tables, proxy config or Internet host is used.
func TestDirectWANIsolatedNetwork(t *testing.T) {
	if os.Getenv("REMOTEGATE_DIRECT_NETNS_TEST") != "1" {
		t.Skip("requires isolated network namespace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	baseline4, _ := routeExec(ctx, "ip", "-4", "route", "show", "table", "main")
	baseline6, _ := routeExec(ctx, "ip", "-6", "route", "show", "table", "main")
	proxy, err := net.Listen("tcp4", "127.0.0.1:7892")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy6, err := net.Listen("tcp6", "[::1]:7892")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy6.Close()
	serve := func(listener net.Listener) {
		go func() {
			for {
				c, e := listener.Accept()
				if e != nil {
					return
				}
				go func() {
					defer c.Close()
					c.SetDeadline(time.Now().Add(time.Second))
					buf := make([]byte, 1024)
					c.Read(buf)
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nPROXY")
				}()
			}
		}()
	}
	serve(proxy)
	serve(proxy6)
	d := newDirectWANDialer(Config{})
	d.lock = nil
	d.guard = os.Getenv("REMOTEGATE_GUARD_PATH")
	direct := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: d.DialContext, DisableKeepAlives: true}, Timeout: 3 * time.Second}
	unmarked := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: time.Second}
	get := func(client *http.Client, host string) (string, error) {
		r, err := client.Get("http://" + host + ":18088/")
		if err != nil {
			return "", err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		return string(body), err
	}
	for _, host := range []string{"203.0.113.10", "[2001:db8:30::10]"} {
		body, err := get(unmarked, host)
		if err != nil || body != "PROXY" {
			t.Fatal("proxy control did not traverse interception", host, body, err)
		}
	}
	checkDirect := func() {
		t.Helper()
		for host, want := range map[string]string{"203.0.113.10": "WAN 10.23.1.2", "[2001:db8:30::10]": "WAN 2001:db8:10::2"} {
			body, err := get(direct, host)
			if err != nil || body != want {
				for _, command := range [][]string{{"nft", "list", "table", "inet", "fw4"}, {"ip", "-4", "rule", "show"}, {"ip", "-4", "route", "show", "table", directTable}, {"ip", "-4", "route", "get", "203.0.113.10", "mark", "0x52470001"}} {
					out, _ := routeExec(ctx, command[0], command[1:]...)
					t.Log(out)
				}
				t.Fatal("protected connection did not use WAN", host, body, err)
			}
		}
	}
	checkDirect()
	// Keep real TCP sockets open through rule destruction, without a new dial
	// (which would repair the rules and hide the reload window).
	var persistent []net.Conn
	var readers []*bufio.Reader
	for _, host := range []string{"203.0.113.10:18088", "[2001:db8:30::10]:18088"} {
		c, err := d.DialContext(ctx, "tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		persistent = append(persistent, c)
		readers = append(readers, bufio.NewReader(c))
	}
	checkPersistent := func() {
		t.Helper()
		for i, c := range persistent {
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: fixture\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			r, err := http.ReadResponse(readers[i], nil)
			if err != nil {
				t.Fatal("persistent direct socket interrupted", err)
			}
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil || !strings.HasPrefix(string(body), "WAN ") {
				t.Fatal("persistent socket left WAN", string(body), err)
			}
		}
	}
	checkPersistent()
	for _, host := range []string{"203.0.113.10", "[2001:db8:30::10]"} {
		if body, err := get(unmarked, host); err != nil || body != "PROXY" {
			t.Fatal("direct protection changed unrelated proxy traffic", host, body, err)
		}
	}
	proxy.Close()
	proxy6.Close() // The proxy node is now unavailable.
	for _, host := range []string{"203.0.113.10", "[2001:db8:30::10]"} {
		if body, err := get(unmarked, host); err == nil {
			t.Fatal("dead proxy control unexpectedly worked", body)
		}
	}
	checkDirect()
	if os.Getenv("REMOTEGATE_FIREWALL_BACKEND") == "nft" {
		// A proxy restart flushes its output chains and puts interception first.
		for _, rule := range []string{"flush chain inet fw4 openclash_output", "add rule inet fw4 openclash_output tcp dport 18088 counter redirect to :7892", "flush chain inet fw4 openclash_output_v6", "add rule inet fw4 openclash_output_v6 tcp dport 18088 counter redirect to :7892"} {
			if _, err := routeExec(ctx, "nft", strings.Fields(rule)...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := routeExec(ctx, "nft", "insert", "rule", "inet", "fw4", "nat_output", "tcp", "dport", "18088", "redirect", "to", ":7892"); err != nil {
			t.Fatal(err)
		}
		// NAT rules alone cannot steal a new marked connection either.
		for _, host := range []string{"203.0.113.10:18088", "[2001:db8:30::10]:18088"} {
			c, err := boundDialer("wan0", directMark).DialContext(ctx, "tcp", host)
			if err != nil {
				t.Fatal(err)
			}
			c.SetDeadline(time.Now().Add(2 * time.Second))
			io.WriteString(c, "GET / HTTP/1.1\r\nHost: fixture\r\nConnection: close\r\n\r\n")
			r, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				c.Close()
				t.Fatal("persistent NAT protection failed", err)
			}
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			c.Close()
			if err != nil || !strings.HasPrefix(string(body), "WAN ") {
				t.Fatal(string(body), err)
			}
		}
		for _, rule := range []string{"flush chain inet fw4 mangle_output", "add rule inet fw4 mangle_output tcp dport 18088 meta mark set 0x162"} {
			if _, err := routeExec(ctx, "nft", strings.Fields(rule)...); err != nil {
				t.Fatal(err)
			}
		}
		checkPersistent()
		// No prepare here: new sockets may be blocked by a transient foreign
		// policy route, but must never enter the failed proxy. Existing sockets
		// above continue to work. Reconciliation below restores new connections.
		for _, host := range []string{"203.0.113.10:18088", "[2001:db8:30::10]:18088"} {
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			c, err := boundDialer("wan0", directMark).DialContext(probeCtx, "tcp", host)
			cancel()
			if err != nil {
				continue
			}
			c.SetDeadline(time.Now().Add(3 * time.Second))
			io.WriteString(c, "GET / HTTP/1.1\r\nHost: fixture\r\nConnection: close\r\n\r\n")
			r, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				c.Close()
				t.Fatal("new socket intercepted before reconciliation", err)
			}
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			c.Close()
			if err != nil || !strings.HasPrefix(string(body), "WAN ") {
				t.Fatal("new socket left WAN", string(body), err)
			}
		}
	} else {
		for _, cmd := range []string{"iptables", "ip6tables"} {
			if _, err := routeExec(ctx, cmd, "-t", "nat", "-I", "OUTPUT", "1", "-p", "tcp", "--dport", "18088", "-j", "REDIRECT", "--to-ports", "7892"); err != nil {
				t.Fatal(err)
			}
		}
	}
	checkDirect() // Reconciles automatically after the proxy reload.
	checkDirect() // Repeated setup is idempotent.
	if now, _ := routeExec(ctx, "ip", "-4", "route", "show", "table", "main"); now != baseline4 {
		t.Fatal("IPv4 main routes changed")
	}
	if now, _ := routeExec(ctx, "ip", "-6", "route", "show", "table", "main"); now != baseline6 {
		t.Fatal("IPv6 main routes changed")
	}
	// Reproduce a LAN-fed router which still has unplugged WAN configuration.
	// Renaming this fixture's uplink preserves its real IPv4/IPv6 gateway,
	// while all proxy/TUN interception remains present.
	if _, err = routeExec(ctx, "ip", "link", "set", "wan0", "name", "br-lan"); err != nil {
		t.Fatal(err)
	}
	lanFixture := `{"interface":[{"interface":"wan","up":false,"device":"eth-unused"},{"interface":"wan6","up":false,"device":"eth-unused"},{"interface":"lan","up":true,"l3_device":"br-lan","route":[{"target":"0.0.0.0","mask":0,"nexthop":"10.23.1.1"},{"target":"::","mask":0,"nexthop":"2001:db8:10::1"}]}]}`
	if err = os.WriteFile(os.Getenv("REMOTEGATE_WAN_FIXTURE"), []byte(lanFixture), 0600); err != nil {
		t.Fatal(err)
	}
	checkDirect()
	conn, err := d.DialContext(ctx, "tcp", "203.0.113.10:18088")
	if err != nil {
		t.Fatal("LAN direct connection failed", err)
	}
	if conn.(*directWANConn).iface != "br-lan" {
		t.Fatal("LAN actual egress not reported")
	}
	conn.Close()
	// netifd may omit static LAN defaults. Main-table defaults are valid too.
	if err = os.WriteFile(os.Getenv("REMOTEGATE_WAN_FIXTURE"), []byte(`{"interface":[{"interface":"wan","up":false,"device":"eth-unused"},{"interface":"lan","up":true,"l3_device":"br-lan"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	checkDirect()
	// Losing both WAN and LAN must not fall through to the restored proxy.
	proxy, err = net.Listen("tcp4", "127.0.0.1:7892")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	serve(proxy)
	if err = os.WriteFile(os.Getenv("REMOTEGATE_WAN_FIXTURE"), []byte(`{"interface":[{"interface":"wan","up":false,"device":"eth-unused"},{"interface":"lan","up":false,"l3_device":"br-lan"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = d.DialContext(ctx, "tcp", "203.0.113.10:18088"); err == nil || !strings.Contains(err.Error(), "不回退") {
		t.Fatal("WAN failure did not fail closed", err)
	}
	if _, err = routeExec(ctx, "ip", "link", "set", "br-lan", "name", "wan0"); err != nil {
		t.Fatal(err)
	}
	if _, err = routeExec(ctx, d.guard, "stop"); err != nil {
		t.Fatal(err)
	}
	if rules, _ := routeExec(ctx, "ip", "-4", "rule", "show"); strings.Contains(rules, "138039") || !strings.Contains(rules, "lookup 354") {
		t.Fatal("cleanup changed unrelated policy", rules)
	}
	t.Log("PASS: IPv4/IPv6 WAN and LAN sources, unused WAN with LAN uplink, main-table LAN fallback, healthy/dead proxy controls, TUN policy bypass, firewall reload repair, no uplink without proxy fallback, scoped cleanup")
}

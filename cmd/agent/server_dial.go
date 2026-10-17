package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// Only the control connection uses this resolver. LAN mapping targets continue
// to use the router's local DNS and the private-target checks.
type serverDialer struct {
	lookup     func(context.Context, string) ([]net.IPAddr, error)
	dial       dialContextFunc
	realLookup func(context.Context, string) ([]net.IPAddr, error)
	resolved   func(string, string)
	mu         sync.Mutex
	cacheHost  string
	cache      []net.IPAddr
	cacheUntil time.Time
}

func newServerDialer(cfg Config, resolved func(string, string)) *serverDialer {
	base := boundDialer(cfg.Interface, cfg.Mark).DialContext
	return &serverDialer{lookup: net.DefaultResolver.LookupIPAddr, dial: base, realLookup: func(ctx context.Context, host string) ([]net.IPAddr, error) { return lookupServerDoH(ctx, host, base) }, resolved: resolved}
}

func fakeServerIP(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)
}

func publicDNSName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".home", ".test", ".invalid"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func (d *serverDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(strings.Split(host, "%")[0]) != nil {
		conn, err := d.dial(ctx, network, address)
		if err == nil && d.resolved != nil {
			d.resolved(host, "system")
		}
		return conn, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lookupCtx, stopLookup := context.WithTimeout(ctx, 3*time.Second)
	addresses, lookupErr := d.lookup(lookupCtx, host)
	stopLookup()
	var usable []net.IPAddr
	fake := false
	for _, address := range addresses {
		if fakeServerIP(address.IP) {
			fake = true
			continue
		}
		usable = append(usable, address)
	}
	mode := "system"
	if len(usable) == 0 && publicDNSName(host) && (fake || lookupErr != nil || len(addresses) == 0) {
		d.mu.Lock()
		if d.cacheHost == host && time.Now().Before(d.cacheUntil) {
			usable = append(usable, d.cache...)
		}
		d.mu.Unlock()
		if len(usable) == 0 {
			usable, err = d.realLookup(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("系统 DNS 返回虚拟地址或解析失败，加密 DNS 解析 %s 失败：%w", host, err)
			}
			for _, address := range usable {
				if fakeServerIP(address.IP) {
					return nil, errors.New("加密 DNS 仍返回 Fake-IP，已停止连接")
				}
			}
			if len(usable) > 0 {
				d.mu.Lock()
				d.cacheHost, d.cache, d.cacheUntil = host, append([]net.IPAddr(nil), usable...), time.Now().Add(time.Minute)
				d.mu.Unlock()
			}
		}
		mode = "encrypted"
	}
	if len(usable) == 0 {
		if lookupErr != nil {
			return nil, lookupErr
		}
		return nil, errors.New("服务器域名未解析到可连接的真实地址")
	}
	conn, err := dialServerAddresses(ctx, d.dial, network, port, usable)
	if err == nil && d.resolved != nil {
		peer, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		d.resolved(peer, mode)
	}
	return conn, err
}

// Stagger attempts so an unavailable IPv6 path cannot block a working IPv4
// address (or vice versa). Losing connections are closed after cancellation.
func dialServerAddresses(ctx context.Context, dial dialContextFunc, network, port string, addresses []net.IPAddr) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	count := 0
	for _, address := range addresses {
		if network == "tcp4" && address.IP.To4() == nil || network == "tcp6" && address.IP.To4() != nil {
			continue
		}
		if count == 8 {
			break
		}
		delay := time.Duration(count) * 200 * time.Millisecond
		count++
		go func(address net.IPAddr, delay time.Duration) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			ip := address.IP.String()
			if address.Zone != "" {
				ip += "%" + address.Zone
			}
			conn, err := dial(ctx, network, net.JoinHostPort(ip, port))
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}(address, delay)
	}
	if count == 0 {
		return nil, errors.New("服务器没有适用于当前网络的地址")
	}
	var lastErr error
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-results:
			if result.err == nil {
				return result.conn, nil
			}
			lastErr = result.err
		}
	}
	return nil, lastErr
}

func lookupServerDoH(ctx context.Context, host string, dial dialContextFunc) ([]net.IPAddr, error) {
	var lastErr error
	for _, bootstrap := range []string{"223.5.5.5:443", "223.6.6.6:443"} {
		transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 3 * time.Second, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) { return dial(ctx, network, bootstrap) }}
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		type answer struct {
			ips []net.IPAddr
			err error
		}
		answers := make(chan answer, 2)
		for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
			go func(kind dnsmessage.Type) {
				ips, err := queryServerDoH(queryCtx, client, host, kind)
				answers <- answer{ips, err}
			}(kind)
		}
		var ips []net.IPAddr
		for i := 0; i < 2; i++ {
			result := <-answers
			ips = append(ips, result.ips...)
			if result.err != nil {
				lastErr = result.err
			}
		}
		cancel()
		transport.CloseIdleConnections()
		if len(ips) > 0 {
			return ips, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的 A 或 AAAA 记录")
	}
	return nil, lastErr
}

func queryServerDoH(ctx context.Context, client *http.Client, host string, kind dnsmessage.Type) ([]net.IPAddr, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, err
	}
	query := dnsmessage.Message{Header: dnsmessage.Header{RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: kind, Class: dnsmessage.ClassINET}}}
	raw, err := query.Pack()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://dns.alidns.com/dns-query?dns="+base64.RawURLEncoding.EncodeToString(raw), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/dns-message")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("加密 DNS 返回 HTTP %d", response.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil {
		return nil, err
	}
	if len(raw) == 65536 {
		return nil, errors.New("加密 DNS 响应过大")
	}
	var message dnsmessage.Message
	if err = message.Unpack(raw); err != nil {
		return nil, err
	}
	if !message.Response || message.Truncated || message.ID != query.ID || message.RCode != dnsmessage.RCodeSuccess || len(message.Questions) != 1 {
		return nil, errors.New("加密 DNS 响应与查询不匹配")
	}
	question := message.Questions[0]
	if question.Type != kind || question.Class != dnsmessage.ClassINET || !strings.EqualFold(question.Name.String(), name.String()) {
		return nil, errors.New("加密 DNS 响应与查询不匹配")
	}
	allowed := map[string]bool{strings.ToLower(name.String()): true}
	for i := 0; i < 16; i++ {
		changed := false
		for _, resource := range message.Answers {
			if cname, ok := resource.Body.(*dnsmessage.CNAMEResource); ok && resource.Header.Class == dnsmessage.ClassINET && allowed[strings.ToLower(resource.Header.Name.String())] && !allowed[strings.ToLower(cname.CNAME.String())] {
				allowed[strings.ToLower(cname.CNAME.String())] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	var ips []net.IPAddr
	for _, resource := range message.Answers {
		if !allowed[strings.ToLower(resource.Header.Name.String())] || resource.Header.Class != dnsmessage.ClassINET {
			continue
		}
		var ip net.IP
		switch value := resource.Body.(type) {
		case *dnsmessage.AResource:
			if kind == dnsmessage.TypeA {
				ip = net.IP(value.A[:])
			}
		case *dnsmessage.AAAAResource:
			if kind == dnsmessage.TypeAAAA {
				ip = net.IP(value.AAAA[:])
			}
		}
		if ip != nil && ip.IsGlobalUnicast() && !fakeServerIP(ip) {
			ips = append(ips, net.IPAddr{IP: ip})
		}
	}
	return ips, nil
}

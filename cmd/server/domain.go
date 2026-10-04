package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/local/remotegate/internal/config"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (a *app) domains(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, a.store.Domains())
	case "POST", "PUT":
		var d config.DomainSettings
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&d) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		created, err := a.store.PutDomain(d)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, created)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func (a *app) domainByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := a.store.DeleteDomain(strings.TrimPrefix(r.URL.Path, "/api/admin/domains/")); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(204)
}
func (a *app) domainCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var d config.DomainSettings
	for _, candidate := range a.store.Domains() {
		if candidate.ID == r.URL.Query().Get("id") {
			d = candidate
			break
		}
	}
	if d.BaseDomain == "" {
		http.Error(w, "域名不存在，请刷新列表", 404)
		return
	}
	targets, err := domainCheckTargets(d, a.store.Snapshot().Mappings, a.consoleHost, r.URL.Query().Get("host"), r.URL.Query().Get("port"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	rows := []map[string]any{}
	var wg sync.WaitGroup
	resultRows := make([]map[string]any, len(targets))
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target domainCheckTarget) {
			defer wg.Done()
			row := map[string]any{"host": target.Host, "port": target.Port, "kind": target.Kind, "matches": false, "expectedIP": d.ServerIP}
			ips, err := net.DefaultResolver.LookupHost(ctx, target.Host)
			if err != nil {
				row["error"] = "域名未解析或 DNS 查询失败"
				resultRows[i] = row
				return
			}
			row["addresses"] = ips
			for _, value := range ips {
				if expected := net.ParseIP(d.ServerIP); expected != nil && expected.Equal(net.ParseIP(value)) {
					row["matches"] = true
				}
			}
			if target.Kind != "wildcard" && target.HTTPS {
				certResult := map[string]any{"valid": false, "error": "线上证书检测仅支持公网地址"}
				for _, value := range ips {
					ip := net.ParseIP(value)
					if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
						continue
					}
					dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 4 * time.Second}, Config: &tls.Config{ServerName: target.Host, MinVersion: tls.VersionTLS12}}
					conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(target.Port)))
					if e != nil {
						certResult = map[string]any{"valid": false, "error": e.Error()}
						continue
					}
					peer := conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
					conn.Close()
					certResult = map[string]any{"valid": true, "issuer": peer.Issuer.CommonName, "expiresAt": peer.NotAfter, "names": peer.DNSNames, "daysRemaining": int(time.Until(peer.NotAfter).Hours() / 24)}
					break
				}
				row["certificate"] = certResult
			}
			resultRows[i] = row
		}(i, target)
	}
	wg.Wait()
	rows = append(rows, resultRows...)
	writeJSON(w, map[string]any{"dns": rows, "checkedAt": time.Now(), "serverIP": d.ServerIP})
}

type domainCheckTarget struct {
	Host  string
	Port  int
	Kind  string
	HTTPS bool
}

func domainCheckTargets(d config.DomainSettings, mappings []config.Mapping, consoleHost, host, port string) ([]domainCheckTarget, error) {
	belongs := func(v string) bool { return v == d.BaseDomain || strings.HasSuffix(v, "."+d.BaseDomain) }
	if host != "" {
		host = strings.ToLower(strings.TrimSpace(host))
		if !checkHostPattern.MatchString(host) || !belongs(host) {
			return nil, errors.New("检测域名必须是该主域名下的具体域名")
		}
		n := 443
		if port != "" {
			v, e := strconv.Atoi(port)
			if e != nil || v < 1 || v > 65535 {
				return nil, errors.New("检测端口范围为 1–65535")
			}
			n = v
		}
		return []domainCheckTarget{{Host: host, Port: n, Kind: "custom", HTTPS: true}}, nil
	}
	out := []domainCheckTarget{}
	seen := map[string]bool{}
	add := func(t domainCheckTarget) {
		key := t.Host + ":" + strconv.Itoa(t.Port)
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	if consoleHost != "" && belongs(consoleHost) {
		add(domainCheckTarget{Host: consoleHost, Port: 443, Kind: "console", HTTPS: true})
	}
	for _, m := range mappings {
		if m.Enabled && belongs(m.Host) {
			n := m.PublicPort
			if n == 0 {
				n = 443
			}
			add(domainCheckTarget{Host: m.Host, Port: n, Kind: "mapping", HTTPS: m.PublicScheme != "http"})
			if len(out) >= 10 {
				break
			}
		}
	}
	if len(out) == 0 {
		add(domainCheckTarget{Host: d.BaseDomain, Port: 443, Kind: "root", HTTPS: true})
	}
	add(domainCheckTarget{Host: "rg-check-" + time.Now().Format("150405") + "." + d.BaseDomain, Port: 443, Kind: "wildcard"})
	return out, nil
}

var checkHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

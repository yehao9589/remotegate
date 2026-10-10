package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
					result, e := inspectHTTPSCertificate(ctx, net.JoinHostPort(ip.String(), strconv.Itoa(target.Port)), target.Host, nil)
					if e != nil {
						certResult = map[string]any{"valid": false, "error": e.Error()}
						continue
					}
					certResult = result
					if result["valid"] == true {
						break
					}
				}
				row["certificate"] = certResult
			}
			resultRows[i] = row
		}(i, target)
	}
	wg.Wait()
	if r.Context().Err() != nil {
		return
	}
	rows = append(rows, resultRows...)
	checkedAt := time.Now()
	checks := []config.PublicHTTPSCheck{}
	for _, row := range rows {
		if row["kind"] == "wildcard" {
			continue
		}
		cert, hasCertificate := row["certificate"].(map[string]any)
		if !hasCertificate && row["error"] == nil {
			continue
		}
		check := config.PublicHTTPSCheck{CheckedAt: checkedAt}
		check.Host, _ = row["host"].(string)
		check.Port, _ = row["port"].(int)
		check.Addresses, _ = row["addresses"].([]string)
		check.Valid, _ = cert["valid"].(bool)
		check.Error, _ = cert["error"].(string)
		if message, ok := row["error"].(string); ok {
			check.Error = message
		}
		check.Issuer, _ = cert["issuer"].(string)
		check.Names, _ = cert["names"].([]string)
		check.NotBefore, _ = cert["notBefore"].(time.Time)
		check.ExpiresAt, _ = cert["expiresAt"].(time.Time)
		checks = append(checks, check)
	}
	if err := a.store.SetDomainHTTPSChecks(d.ID, checks); err != nil {
		http.Error(w, "保存检测结果失败，请重新检测："+err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"dns": rows, "checkedAt": checkedAt, "serverIP": d.ServerIP})
}

// Read only public TLS metadata, including the certificate returned by an
// incorrect site. We explicitly verify trust, expiry and hostname before ever
// reporting success; no HTTP request or credential is sent over this connection.
func inspectHTTPSCertificate(ctx context.Context, address, host string, roots *x509.CertPool) (map[string]any, error) {
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 4 * time.Second}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	peers := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(peers) == 0 {
		return nil, errors.New("公网入口没有返回证书")
	}
	peer := peers[0]
	intermediates := x509.NewCertPool()
	for _, certificate := range peers[1:] {
		intermediates.AddCert(certificate)
	}
	_, err = peer.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates})
	result := map[string]any{"valid": err == nil, "issuer": peer.Issuer.CommonName, "notBefore": peer.NotBefore, "expiresAt": peer.NotAfter, "names": peer.DNSNames, "daysRemaining": int(time.Until(peer.NotAfter).Hours() / 24)}
	if err != nil {
		result["error"] = "证书验证未通过：" + err.Error()
	}
	return result, nil
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
		port := d.RootHTTPSPort
		if port == 0 {
			port = 443
		}
		add(domainCheckTarget{Host: d.BaseDomain, Port: port, Kind: "root", HTTPS: true})
	}
	add(domainCheckTarget{Host: "rg-check-" + time.Now().Format("150405") + "." + d.BaseDomain, Port: 443, Kind: "wildcard"})
	return out, nil
}

var checkHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

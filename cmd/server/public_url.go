package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type publicURLKey struct{}

var publicHostnamePattern = regexp.MustCompile(`^[a-z0-9.-]+$`)

// PUBLIC_URL is an explicit external origin, never inferred from forwarded headers.
func parsePublicURL(value string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, errors.New("PUBLIC_URL 需为完整访问地址，例如 https://gate.example.com，不含后台路径、账号、查询参数或片段")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || (net.ParseIP(host) == nil && (!publicHostnamePattern.MatchString(host) || strings.Contains(host, "..") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, "."))) {
		return nil, errors.New("PUBLIC_URL 的主机名无效")
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("PUBLIC_URL 的端口需为 1–65535")
		}
		port = strconv.Itoa(n)
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Path = ""
	return u, nil
}

func loopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func netLoopbackHost(host string) bool {
	ip := net.ParseIP(hostOnly(host))
	return ip != nil && ip.IsLoopback()
}

func publicURLForRequest(r *http.Request) *url.URL {
	u, _ := r.Context().Value(publicURLKey{}).(*url.URL)
	return u
}

func matchesPublicOrigin(r *http.Request) bool {
	expected := publicURLForRequest(r)
	if expected == nil || !loopbackPeer(r) {
		return false
	}
	actual, err := parsePublicURL(r.Header.Get("Origin"))
	return err == nil && actual.String() == expected.String()
}

func (a *app) withPublicURL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.publicURL != nil && loopbackPeer(r) {
			r = r.WithContext(context.WithValue(r.Context(), publicURLKey{}, a.publicURL))
		}
		next.ServeHTTP(w, r)
	})
}

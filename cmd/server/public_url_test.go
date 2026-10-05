package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPSProxyWithDefaultHostAndNoForwardedProto(t *testing.T) {
	a := freshAuthApp(t)
	backend := httptest.NewServer(a.routes())
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = "127.0.0.1"
		r.Header.Del("X-Forwarded-Proto")
	}
	frontend := httptest.NewTLSServer(proxy)
	defer frontend.Close()
	a.publicURL, _ = parsePublicURL(frontend.URL)
	a.consoleHost = "gate.example.com"
	client := frontend.Client()
	client.Jar, _ = cookiejar.New(nil)
	call := func(method, path, body, origin string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, frontend.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := call("POST", "/api/auth/setup", setupBody, frontend.URL)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(response.Cookies()) != 1 || !response.Cookies()[0].Secure {
		t.Fatalf("proxy setup: %d %s", response.StatusCode, data)
	}
	response = call("GET", "/admin", "", "")
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(data), "setupEntry") {
		t.Fatalf("proxy console: %d", response.StatusCode)
	}
	response = call("GET", "/api/admin/state", "", "")
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("proxy session cookie was not retained")
	}
	response = call("POST", "/api/admin/security", `{"action":"entry","entryPath":"/safe-panel","currentPassword":"test-password-123"}`, "https://evil.example")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin proxy write accepted")
	}
	response = call("POST", "/api/admin/security", `{"action":"entry","entryPath":"/safe-panel","currentPassword":"test-password-123"}`, frontend.URL)
	response.Body.Close()
	if response.StatusCode != 200 || a.auth.entryPath() != "/safe-panel" {
		t.Fatal("same-origin proxy write failed")
	}
}

func proxyAuthCall(a *app, method, path, body, origin, remote string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestDefaultLocalProxyInstallAndSecurity(t *testing.T) {
	a := freshAuthApp(t)
	a.consoleHost = "gate.example.com"
	a.publicURL, _ = parsePublicURL("https://gate.example.com")
	call := func(method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return proxyAuthCall(a, method, path, body, origin, "127.0.0.1:45000", cookie)
	}
	// The proxy replaces Host and does not send X-Forwarded-Proto.
	requireStatus(t, call("GET", "/install", "", "", nil), 200)
	w := call("POST", "/api/auth/setup", setupBody, "https://gate.example.com:443", nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("external HTTPS must issue a protected cookie")
	}
	requireStatus(t, call("GET", "/admin", "", "", nil), 200)
	requireStatus(t, call("GET", "/install", "", "", nil), 404)
	entryBody := `{"action":"entry","entryPath":"/private-panel","currentPassword":"test-password-123"}`
	requireStatus(t, call("POST", "/api/admin/security", entryBody, "https://evil.example", cookie), 403)
	if a.auth.entryPath() != "/admin" {
		t.Fatal("cross-site request changed the entry")
	}
	requireStatus(t, call("POST", "/api/admin/security", entryBody, "https://gate.example.com", cookie), 200)
	requireStatus(t, call("GET", "/api/admin/state", "", "", cookie), 401)
	requireStatus(t, call("GET", "/admin", "", "", nil), 404)
	requireStatus(t, call("GET", "/private-panel", "", "", nil), 200)
	w = call("POST", "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, "https://gate.example.com", nil)
	requireStatus(t, w, 200)
	cookie = w.Result().Cookies()[0]
	requireStatus(t, call("POST", "/api/auth/logout", `{}`, "https://gate.example.com", cookie), 204)
	requireStatus(t, call("GET", "/api/admin/state", "", "", cookie), 401)
}

func TestPublicURLRejectsUntrustedOriginsAndPeers(t *testing.T) {
	for _, tc := range []struct{ name, origin, remote string }{
		{"other site", "https://evil.example", "127.0.0.1:45000"},
		{"other port", "https://gate.example.com:8443", "127.0.0.1:45000"},
		{"wrong scheme", "http://gate.example.com", "127.0.0.1:45000"},
		{"subdomain", "https://other.gate.example.com", "127.0.0.1:45000"},
		{"external peer", "https://gate.example.com", "198.51.100.20:45000"},
		{"private LAN peer", "https://gate.example.com", "192.168.1.20:45000"},
		{"null origin", "null", "127.0.0.1:45000"},
		{"origin with credentials", "https://user@gate.example.com", "127.0.0.1:45000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := freshAuthApp(t)
			a.publicURL, _ = parsePublicURL("https://gate.example.com")
			w := proxyAuthCall(a, "POST", "/api/auth/setup", setupBody, tc.origin, tc.remote, nil)
			requireStatus(t, w, 403)
			if a.auth.initialized() {
				t.Fatal("untrusted request created an administrator")
			}
		})
	}
	a := freshAuthApp(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/auth/setup", strings.NewReader(setupBody))
	r.RemoteAddr = "127.0.0.1:45000"
	r.Header.Set("Origin", "https://gate.example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Host", "gate.example.com")
	r.Header.Set("Forwarded", "host=gate.example.com;proto=https")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 403)
	// Explicit cross-site metadata is rejected even if an Origin happens to match.
	a.publicURL, _ = parsePublicURL("https://gate.example.com")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 403)
}

func TestHTTPPublicURLAndIPv6Proxy(t *testing.T) {
	a := freshAuthApp(t)
	a.publicURL, _ = parsePublicURL("http://gate.example.com:8080")
	w := proxyAuthCall(a, "POST", "/api/auth/setup", setupBody, "http://gate.example.com:8080", "[::1]:45000", nil)
	requireStatus(t, w, 200)
	if w.Result().Cookies()[0].Secure {
		t.Fatal("configured HTTP was incorrectly treated as HTTPS")
	}
}

func TestParsePublicURL(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"https://Gate.Example.com:443/", "https://gate.example.com"},
		{"http://localhost:80", "http://localhost"},
		{"https://gate.example.com:8443", "https://gate.example.com:8443"},
		{"http://[::1]:18088", "http://[::1]:18088"},
	} {
		u, err := parsePublicURL(tc.value)
		if err != nil || u.String() != tc.want {
			t.Fatalf("%q: %v, %v", tc.value, u, err)
		}
	}
	for _, value := range []string{"", "gate.example.com", "ftp://gate.example.com", "https://gate.example.com/admin", "https://user:password@gate.example.com", "https://gate.example.com?", "https://gate.example.com?q=1", "https://gate.example.com#admin", "https://*.example.com", "https://gate.example.com:0", "https://gate.example.com:65536", "https://gate.example.com/%2f"} {
		if _, err := parsePublicURL(value); err == nil {
			t.Fatalf("accepted invalid PUBLIC_URL %q", value)
		}
	}
}

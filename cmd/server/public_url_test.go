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
		{"unsupported scheme", "ftp://gate.example.com", "127.0.0.1:45000"},
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

func TestLocalProxyProtocolCompatibility(t *testing.T) {
	for _, configured := range []string{"http://gate.example.com", "https://gate.example.com", ""} {
		for _, origin := range []string{"http://gate.example.com:80", "https://gate.example.com:443"} {
			t.Run(configured+"/"+origin, func(t *testing.T) {
				a := freshAuthApp(t)
				a.consoleHost = "gate.example.com"
				if configured != "" {
					a.publicURL, _ = parsePublicURL(configured)
				}
				requireStatus(t, proxyAuthCall(a, "GET", "/install", "", "", "127.0.0.1:45000", nil), 200)
				call := func(path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
					return proxyAuthCall(a, "POST", path, body, origin, "127.0.0.1:45000", cookie)
				}
				w := call("/api/auth/setup", setupBody, nil)
				requireStatus(t, w, 200)
				cookie := w.Result().Cookies()[0]
				secure := strings.HasPrefix(origin, "https:")
				if cookie.Secure != secure || (secure && cookie.Name != adminCookie) || (!secure && cookie.Name != httpAdminCookie) {
					t.Fatalf("cookie must follow actual protocol: %+v", cookie)
				}
				get := proxyAuthCall(a, "GET", "/api/admin/state", "", "", "127.0.0.1:45000", cookie)
				requireStatus(t, get, 200)
				requireStatus(t, call("/api/admin/security", `{"action":"entry","entryPath":"/new-panel","currentPassword":"test-password-123"}`, cookie), 200)
				w = call("/api/auth/login", `{"username":"owner","password":"test-password-123"}`, nil)
				requireStatus(t, w, 200)
				cookie = w.Result().Cookies()[0]
				requireStatus(t, call("/api/auth/logout", `{}`, cookie), 204)
				requireStatus(t, proxyAuthCall(a, "GET", "/api/admin/state", "", "", "127.0.0.1:45000", cookie), 401)
			})
		}
	}
}

func TestConsoleHostFallbackKeepsProxyTrustBoundaries(t *testing.T) {
	for _, tc := range []struct{ origin, remote string }{
		{"https://evil.example", "127.0.0.1:45000"},
		{"https://other.gate.example.com", "127.0.0.1:45000"},
		{"http://gate.example.com:8080", "127.0.0.1:45000"},
		{"https://gate.example.com", "192.168.1.20:45000"},
		{"http://gate.example.com", "198.51.100.20:45000"},
	} {
		a := freshAuthApp(t)
		a.consoleHost = "gate.example.com"
		requireStatus(t, proxyAuthCall(a, "POST", "/api/auth/setup", setupBody, tc.origin, tc.remote, nil), 403)
		if a.auth.initialized() {
			t.Fatal("untrusted request created an administrator")
		}
	}
	// An explicit custom-port PUBLIC_URL takes priority over the console host.
	a := freshAuthApp(t)
	a.consoleHost = "gate.example.com"
	a.publicURL, _ = parsePublicURL("https://gate.example.com:8443")
	for _, origin := range []string{"http://gate.example.com", "https://gate.example.com", "http://gate.example.com:8080"} {
		requireStatus(t, proxyAuthCall(a, "POST", "/api/auth/setup", setupBody, origin, "127.0.0.1:45000", nil), 403)
	}
	for _, origin := range []string{"http://gate.example.com:8443", "https://gate.example.com:8443"} {
		requireStatus(t, proxyAuthCall(a, "POST", "/api/auth/setup", "{", origin, "127.0.0.1:45000", nil), 400)
	}
	for _, configured := range []string{"", "https://gate.example.com"} {
		a := freshAuthApp(t)
		a.consoleHost = "gate.example.com"
		if configured != "" {
			a.publicURL, _ = parsePublicURL(configured)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1/api/auth/setup", strings.NewReader(setupBody))
		r.RemoteAddr = "127.0.0.1:45000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://gate.example.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		requireStatus(t, w, 403)
	}
}

func TestHTTPOriginControlsCookieDespiteHTTPSConfiguration(t *testing.T) {
	a := freshAuthApp(t)
	a.publicURL, _ = parsePublicURL("https://gate.example.com")
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/auth/setup", strings.NewReader(setupBody))
	r.RemoteAddr = "127.0.0.1:45000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://gate.example.com")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 200)
	if w.Result().Cookies()[0].Secure {
		t.Fatal("HTTP browser cannot retain a Secure session cookie")
	}
}

type proxyTestTransport struct {
	next                    http.RoundTripper
	httpTarget, httpsTarget *url.URL
}

func (p proxyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.URL.Host = p.httpTarget.Host
	if r.URL.Scheme == "https" {
		copy.URL.Host = p.httpsTarget.Host
	}
	copy.Host = "gate.example.com"
	return p.next.RoundTrip(copy)
}

func TestBrowserSessionSwitchesProtocolsThroughRealProxy(t *testing.T) {
	a := freshAuthApp(t)
	a.consoleHost = "gate.example.com"
	a.publicURL, _ = parsePublicURL("https://gate.example.com")
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
	httpFrontend := httptest.NewServer(proxy)
	defer httpFrontend.Close()
	httpsFrontend := httptest.NewTLSServer(proxy)
	defer httpsFrontend.Close()
	httpTarget, _ := url.Parse(httpFrontend.URL)
	httpsTarget, _ := url.Parse(httpsFrontend.URL)
	client := httpsFrontend.Client()
	client.Transport = proxyTestTransport{client.Transport, httpTarget, httpsTarget}
	client.Jar, _ = cookiejar.New(nil)
	call := func(scheme, method, path, body string, want int) *http.Response {
		t.Helper()
		origin := scheme + "://gate.example.com"
		r, _ := http.NewRequest(method, origin+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Admin-Entry", a.auth.entryPath())
		if method == "POST" {
			r.Header.Set("Origin", origin)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", scheme, path, response.StatusCode, want, data)
		}
		return response
	}
	call("http", "POST", "/api/auth/setup", setupBody, 200)
	call("http", "GET", "/api/admin/state", "", 200)
	secureCookie := call("https", "POST", "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, 200).Cookies()[0]
	call("https", "GET", "/api/admin/state", "", 200)
	httpCookie := call("http", "POST", "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, 200).Cookies()[0]
	if httpCookie.Name == secureCookie.Name || httpCookie.Secure || !secureCookie.Secure {
		t.Fatal("protocol switch must not overwrite the HTTPS cookie")
	}
	u, _ := url.Parse("http://gate.example.com")
	for _, cookie := range client.Jar.Cookies(u) {
		if cookie.Name == secureCookie.Name {
			t.Fatal("HTTPS session was sent over HTTP")
		}
	}
	call("http", "GET", "/api/admin/state", "", 200)
	call("https", "POST", "/api/auth/logout", `{}`, 204)
	call("https", "GET", "/api/admin/state", "", 401)
	call("http", "GET", "/api/admin/state", "", 401)
	call("http", "POST", "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, 200)
	call("https", "POST", "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, 200)
	call("https", "POST", "/api/admin/security", `{"action":"password","currentPassword":"test-password-123","newPassword":"updated-password-456","confirmPassword":"updated-password-456"}`, 200)
	call("http", "GET", "/api/admin/state", "", 401)
	call("https", "GET", "/api/admin/state", "", 401)
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

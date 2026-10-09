package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConsoleFirstVisibleScreen(t *testing.T) {
	a := freshAuthApp(t)
	assertScreen := func(path, panel string, cookie *http.Cookie) {
		t.Helper()
		w := authCall(a, path, "", "", cookie)
		requireStatus(t, w, 200)
		page := w.Body.String()
		match := regexp.MustCompile(`<script id="authInitialState" type="application/json">(.*?)</script>`).FindStringSubmatch(page)
		if len(match) != 2 {
			t.Fatal("missing initial state")
		}
		var initial struct{ Panel, EntryPath string }
		if err := json.Unmarshal([]byte(match[1]), &initial); err != nil || initial.Panel != panel {
			t.Fatalf("initial screen: %s, error: %v", match[1], err)
		}
		if panel == "workbench" {
			if initial.EntryPath != "/private-panel" || !strings.Contains(page, `<div id="authScreen" hidden>`) || !strings.Contains(page, `<div id="shell">`) {
				t.Fatal("authenticated page must show workbench immediately")
			}
		} else if !strings.Contains(page, `<section id="`+panel+`">`) || !strings.Contains(page, `<div id="shell" hidden>`) || initial.EntryPath != "" {
			t.Fatal("incorrect initial auth panel or exposed entry path")
		}
		if strings.Contains(page, "正在检查安装状态") || strings.Contains(match[1], "password") || strings.Contains(match[1], "session") {
			t.Fatal("initial page contains old transition or credentials")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("session-dependent HTML must not be cached")
		}
		if w.Header().Get("Vary") != "Cookie" {
			t.Fatal("session-dependent HTML must distinguish browser cookies")
		}
	}
	assertScreen("/install", "firstInstall", nil)
	assertScreen("/", "firstInstall", nil)
	w := authCall(a, "/api/auth/setup", `{"username":"owner","password":"test-password-123","confirmPassword":"test-password-123","entryPath":"/private-panel"}`, "", nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	assertScreen("/private-panel", "login", nil)
	assertScreen("/private-panel", "workbench", cookie)
	assertScreen("/private-panel/", "workbench", cookie)
	invalid := &http.Cookie{Name: httpAdminCookie, Value: "forged-token"}
	assertScreen("/private-panel", "login", invalid)
	a.auth.mu.Lock()
	a.auth.sessions[sha256.Sum256([]byte(cookie.Value))] = adminSession{user: "owner", expires: time.Now().Add(-time.Minute)}
	a.auth.mu.Unlock()
	assertScreen("/private-panel", "login", cookie)
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 401)
	for _, path := range []string{"/", "/install", "/wrong-entry"} {
		requireStatus(t, authCall(a, path, "", "", nil), 404)
	}
}

func TestConsoleInitialScreenThroughLoopbackProxy(t *testing.T) {
	a := freshAuthApp(t)
	a.consoleHost = "console.example.com"
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `<section id="login">`) {
		t.Fatal("default local reverse proxy should render the login screen")
	}
}

func TestConsoleRefreshPreservesHTTPSCookieThroughDefaultProxy(t *testing.T) {
	a := freshAuthApp(t)
	a.consoleHost = "console.example.com"
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	// A default local proxy replaces Host and does not forward the TLS scheme.
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/auth/login", strings.NewReader(`{"username":"owner","password":"test-password-123"}`))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://console.example.com")
	r.Header.Set("X-Admin-Entry", "/admin")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != adminCookie || !cookies[0].Secure {
		t.Fatal("HTTPS login must create a secure session cookie")
	}
	for i := 0; i < 3; i++ {
		for _, path := range []string{"/admin", "/api/auth/status", "/api/admin/state"} {
			r = httptest.NewRequest("GET", "http://127.0.0.1"+path, nil)
			r.RemoteAddr = "127.0.0.1:12345"
			r.AddCookie(cookies[0])
			w = httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			requireStatus(t, w, 200)
			if path == "/admin" && !strings.Contains(w.Body.String(), `"panel":"workbench"`) {
				t.Fatal("refresh lost the secure session through the proxy")
			}
			if path == "/api/auth/status" && !strings.Contains(w.Body.String(), `"authenticated":true`) {
				t.Fatal("status lost the secure session through the proxy")
			}
		}
	}
}

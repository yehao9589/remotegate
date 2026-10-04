package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
)

func freshAuthApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	manager, err := openAdminManager(filepath.Join(dir, "admin.json"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &app{auth: manager, store: store, hub: hub.New()}
}
func authCall(a *app, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	method := http.MethodPost
	if body == "" {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, "http://console.localhost"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if a.auth != nil {
		r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

const setupBody = `{"username":"owner","password":"test-password-123","confirmPassword":"test-password-123"}`

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestFirstInstallLifecycle(t *testing.T) {
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/admin/state", "", "", nil), 409)
	w := authCall(a, "/api/auth/status", "", "", nil)
	requireStatus(t, w, 200)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("auth status must use JSON content type")
	}
	if !strings.Contains(w.Body.String(), `"initialized":false`) {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/", "/install"} {
		w = authCall(a, path, "", "", nil)
		requireStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `id="setupForm"`) {
			t.Fatal("setup page missing")
		}
	}
	w = authCall(a, "/api/auth/setup", setupBody, "http://console.localhost", nil)
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("session missing")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 43200 {
		t.Fatalf("unsafe session: %+v", cookie)
	}
	original, err := os.ReadFile(a.auth.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(original), "test-password-123") || !strings.Contains(string(original), "$2a$") {
		t.Fatal("password not hashed")
	}
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 200)
	w = authCall(a, "/api/auth/status", "", "", cookie)
	if !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 409)
	saved, _ := os.ReadFile(a.auth.path)
	if string(saved) != string(original) {
		t.Fatal("administrator overwritten")
	}
	requireStatus(t, authCall(a, "/api/auth/logout", "{}", "https://evil.example", cookie), 403)
	requireStatus(t, authCall(a, "/api/auth/logout", "{}", "", cookie), 204)
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 401)
	restored, err := openAdminManager(a.auth.path, "other", "different")
	if err != nil {
		t.Fatal(err)
	}
	if !restored.verify("owner", "test-password-123") || restored.verify("other", "different") {
		t.Fatal("persisted account not used")
	}
	a.auth = restored
	requireStatus(t, authCall(a, "/api/auth/login", `{"username":"owner","password":"wrong"}`, "", nil), 401)
	w = authCall(a, "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, "", nil)
	requireStatus(t, w, 200)
	cookie = w.Result().Cookies()[0]
	key := sha256.Sum256([]byte(cookie.Value))
	a.auth.sessions[key] = adminSession{user: "owner", expires: time.Now().Add(-time.Second)}
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 401)
}

func TestSetupRejectsInvalidRequestsWithoutSaving(t *testing.T) {
	cases := []struct {
		name, body, origin string
		status             int
	}{
		{"short password", `{"username":"owner","password":"admin","confirmPassword":"admin"}`, "", 400},
		{"bad username", `{"username":"!","password":"test-password-123","confirmPassword":"test-password-123"}`, "", 400},
		{"confirmation mismatch", `{"username":"owner","password":"test-password-123","confirmPassword":"different"}`, "", 400},
		{"cross origin", setupBody, "https://evil.example", 403},
		{"trailing JSON", setupBody + `{}`, "", 400},
		{"unknown property", `{"username":"owner","password":"test-password-123","confirmPassword":"test-password-123","role":"owner"}`, "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := freshAuthApp(t)
			requireStatus(t, authCall(a, "/api/auth/setup", tc.body, tc.origin, nil), tc.status)
			if a.auth.initialized() {
				t.Fatal("invalid setup saved")
			}
			if _, err := os.Stat(a.auth.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unexpected admin file", err)
			}
		})
	}
	a := freshAuthApp(t)
	req := httptest.NewRequest("POST", "http://console.localhost/api/auth/setup", strings.NewReader(setupBody))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	a.authSetup(w, req)
	requireStatus(t, w, 415)
}

func TestConcurrentInstallCannotOverwriteAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	first, _ := openAdminManager(path, "", "")
	second, _ := openAdminManager(path, "", "")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, m := range []*adminManager{first, second} {
		wg.Add(1)
		go func(m *adminManager) { defer wg.Done(); results <- m.create("owner", "test-password-123", true) }(m)
	}
	wg.Wait()
	close(results)
	success, exists := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errAdminExists) {
			exists++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || exists != 1 {
		t.Fatalf("success=%d exists=%d", success, exists)
	}
	restored, err := openAdminManager(path, "", "")
	if err != nil || !restored.verify("owner", "test-password-123") {
		t.Fatal("published account invalid", err)
	}
}

func TestLegacyMigrationAndWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	m, err := openAdminManager(path, "admin", "admin")
	if err != nil || !m.verify("admin", "admin") {
		t.Fatal("legacy migration failed", err)
	}
	if _, err = openAdminManager(filepath.Join(t.TempDir(), "admin.json"), "admin", ""); err == nil {
		t.Fatal("partial environment accepted")
	}
	if err = os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = openAdminManager(path, "admin", "admin"); err == nil {
		t.Fatal("corrupt account silently replaced")
	}
	a := freshAuthApp(t)
	block := filepath.Join(t.TempDir(), "file")
	if err = os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	a.auth.path = filepath.Join(block, "admin.json")
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 500)
	if a.auth.initialized() {
		t.Fatal("failed save marked installed")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	for i := 0; i < 5; i++ {
		requireStatus(t, authCall(a, "/api/auth/login", `{"username":"owner","password":"wrong"}`, "", nil), 401)
	}
	w := authCall(a, "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, "", nil)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("retry missing")
	}
	a.auth.limits = make(map[string]loginLimit)
	w = authCall(a, "/api/auth/login", `{"username":"owner","password":"test-password-123"}`, "", nil)
	requireStatus(t, w, 200)
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["username"] != "owner" {
		t.Fatal("login result invalid", err)
	}
	cookie := w.Result().Cookies()[0]
	requireStatus(t, authCall(a, "/api/admin/devices", "{}", "https://evil.example", cookie), 403)
}

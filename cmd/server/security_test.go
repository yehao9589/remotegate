package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCustomEntryAndPasswordLifecycle(t *testing.T) {
	a := freshAuthApp(t)
	w := authCall(a, "/api/auth/setup", `{"password":"original-password","confirmPassword":"original-password","entryPath":"/private-panel"}`, "", nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	if !a.auth.verify("admin", "original-password") {
		t.Fatal("default super administrator must be admin")
	}
	for _, path := range []string{"/", "/install", "/admin"} {
		requireStatus(t, authCall(a, path, "", "", nil), 404)
	}
	for _, path := range []string{"/private-panel", "/private-panel/"} {
		requireStatus(t, authCall(a, path, "", "", nil), 200)
	}
	status := authCall(a, "/api/auth/status", "", "", nil)
	if strings.Contains(status.Body.String(), "/private-panel") {
		t.Fatal("public status leaks entry")
	}
	request := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"original-password"}`))
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, request)
	requireStatus(t, w, 404)
	requireStatus(t, authCall(a, "/api/admin/security", "", "", nil), 401)
	w = authCall(a, "/api/admin/security", "", "", cookie)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "Hash") || strings.Contains(w.Body.String(), "original-password") {
		t.Fatal("security metadata leaks password")
	}
	before, _ := os.ReadFile(a.auth.path)
	requireStatus(t, authCall(a, "/api/admin/security", `{"action":"entry","entryPath":"/other-panel","currentPassword":"wrong"}`, "", cookie), 400)
	after, _ := os.ReadFile(a.auth.path)
	if string(before) != string(after) {
		t.Fatal("wrong password changed account")
	}
	requireStatus(t, authCall(a, "/api/admin/security", `{"action":"entry","entryPath":"/other-panel","currentPassword":"original-password"}`, "https://evil.example", cookie), 403)
	w = authCall(a, "/api/admin/security", `{"action":"entry","entryPath":"other-panel/","currentPassword":"original-password"}`, "", cookie)
	requireStatus(t, w, 200)
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 401)
	requireStatus(t, authCall(a, "/private-panel", "", "", nil), 404)
	requireStatus(t, authCall(a, "/other-panel", "", "", nil), 200)
	w = authCall(a, "/api/auth/login", `{"username":"admin","password":"original-password"}`, "", nil)
	requireStatus(t, w, 200)
	cookie = w.Result().Cookies()[0]
	requireStatus(t, authCall(a, "/api/admin/security", `{"action":"password","currentPassword":"original-password","newPassword":"short","confirmPassword":"short"}`, "", cookie), 400)
	requireStatus(t, authCall(a, "/api/admin/security", `{"action":"password","currentPassword":"original-password","newPassword":"updated-password","confirmPassword":"different"}`, "", cookie), 400)
	w = authCall(a, "/api/admin/security", `{"action":"password","currentPassword":"original-password","newPassword":"updated-password","confirmPassword":"updated-password"}`, "", cookie)
	requireStatus(t, w, 200)
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), 401)
	requireStatus(t, authCall(a, "/api/auth/login", `{"username":"admin","password":"original-password"}`, "", nil), 401)
	requireStatus(t, authCall(a, "/api/auth/login", `{"username":"admin","password":"updated-password"}`, "", nil), 200)
	m, err := openAdminManager(a.auth.path, "", "")
	if err != nil || !m.verify("admin", "updated-password") || m.entryPath() != "/other-panel" {
		t.Fatal("settings did not survive restart", err)
	}
	raw, _ := os.ReadFile(a.auth.path)
	if strings.Contains(string(raw), "updated-password") {
		t.Fatal("password persisted as plaintext")
	}
}

func TestEntryValidationAndLegacyCompatibility(t *testing.T) {
	for _, entry := range []string{"/api", "/install", "/downloads", "/healthz", "//evil.test", "https://example.com", "/has space", "/a/b", "/%2fapi", "/xy", "/admin?x=1"} {
		if _, err := normalizeAdminEntry(entry); err == nil {
			t.Fatalf("accepted invalid entry %q", entry)
		}
	}
	a := freshAuthApp(t)
	if err := a.auth.create("admin", "original-password", true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.auth.path)
	var record map[string]any
	_ = json.Unmarshal(raw, &record)
	delete(record, "entryPath")
	raw, _ = json.Marshal(record)
	_ = os.WriteFile(a.auth.path, raw, 0600)
	m, err := openAdminManager(a.auth.path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a.auth = m
	requireStatus(t, authCall(a, "/", "", "", nil), 200)
	requireStatus(t, authCall(a, "/api/auth/login", `{"username":"admin","password":"original-password"}`, "", nil), 200)
}

func TestSecurityWriteFailureKeepsSessionAndPassword(t *testing.T) {
	a := freshAuthApp(t)
	w := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	a.auth.path = a.auth.path + "/missing/admin.json"
	w = authCall(a, "/api/admin/security", `{"action":"password","currentPassword":"test-password-123","newPassword":"updated-password","confirmPassword":"updated-password"}`, "", cookie)
	requireStatus(t, w, 500)
	if !a.auth.verify("owner", "test-password-123") || a.auth.verify("owner", "updated-password") {
		t.Fatal("failed write changed credential")
	}
	requireStatus(t, authCall(a, "/api/admin/state", "", "", cookie), http.StatusOK)
}

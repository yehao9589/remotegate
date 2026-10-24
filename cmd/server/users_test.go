package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/testssh"
	"github.com/local/remotegate/internal/traffic"
)

func createRegular(t *testing.T, a *app, name string, grants []deviceGrant) userView {
	t.Helper()
	u, owners, err := a.auth.putUser(userInput{Username: name, Password: "member-test-password", Enabled: true, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	a.closeRevokedOwners(owners)
	return u
}
func regularCookie(t *testing.T, a *app, name string) *http.Cookie {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": name, "password": "member-test-password"})
	w := authCall(a, "/api/auth/login", string(raw), "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"role":"user"`) {
		t.Fatal("ordinary login role missing")
	}
	return w.Result().Cookies()[0]
}

func TestRegularUsernameRules(t *testing.T) {
	for _, name := range []string{"测试1", "guest01", "用户_01.-", strings.Repeat("中", 32), strings.Repeat("\U00020000", 32)} {
		if !validRegularUsername(name) {
			t.Errorf("rejected valid name %q", name)
		}
	}
	for _, name := range []string{"测试", "ab", "_测试1", "测 试1", "测试/1", "测试@1", "测试😀", "测试\x00", "测试\u200b1", strings.Repeat("中", 33), "abc\xff"} {
		if validRegularUsername(name) {
			t.Errorf("accepted invalid name %q", name)
		}
	}
	if validateAdminInput("测试1", "member-test-password") == nil {
		t.Fatal("regular-user validation changed administrator rules")
	}
	a := freshAuthApp(t)
	if _, _, err := a.auth.putUser(userInput{Username: "测 试1"}); err == nil || err.Error() != regularUsernameHint {
		t.Fatal("missing specific username error", err)
	}
	if _, _, err := a.auth.putUser(userInput{Username: "测试1", Grants: make([]deviceGrant, 1001)}); err == nil || !strings.Contains(err.Error(), "1000 台") {
		t.Fatal("missing specific grant limit error", err)
	}
	if _, _, err := a.auth.putUser(userInput{Username: "测试1", Password: "short"}); err == nil || !strings.Contains(err.Error(), "密码至少") {
		t.Fatal("Chinese username hid password validation", err)
	}
}

func TestChineseRegularUserCreateEditReloadAndLogin(t *testing.T) {
	a := freshAuthApp(t)
	setup := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, setup, 200)
	admin := setup.Result().Cookies()[0]
	device, _, _ := a.store.CreateDevice("authorized router")
	input := userInput{Username: "测试1", Password: "member-test-password", Enabled: true, Grants: []deviceGrant{{DeviceID: device.ID}}}
	raw, _ := json.Marshal(input)
	created := authCall(a, "/api/admin/users", string(raw), "", admin)
	requireStatus(t, created, 200)
	var user userView
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil || user.Username != "测试1" {
		t.Fatal("Chinese account creation failed", err)
	}
	cookie := regularCookie(t, a, "测试1")
	requireStatus(t, authCall(a, "/api/member/state", "", "", cookie), 200)
	requireStatus(t, authCall(a, "/api/admin/users", "", "", cookie), 401)
	input.ID, input.Username, input.Password = user.ID, "测试2", ""
	raw, _ = json.Marshal(input)
	requireStatus(t, authCall(a, "/api/admin/users", string(raw), "", admin), 200)
	requireStatus(t, authCall(a, "/api/member/state", "", "", cookie), 401)
	reloaded, err := openAdminManager(a.auth.path, "", "")
	if err != nil {
		t.Fatal("cannot reopen Chinese user account", err)
	}
	a.auth = reloaded
	cookie = regularCookie(t, a, "测试2")
	if !a.auth.ownerAllows(sha256.Sum256([]byte(cookie.Value)), device.ID, "view") || a.auth.ownerAllows(sha256.Sum256([]byte(cookie.Value)), device.ID, "ssh") {
		t.Fatal("device grant or tool permissions changed")
	}
	if _, _, err := a.auth.putUser(userInput{Username: "测试2"}); err == nil || err.Error() != "账号已存在" {
		t.Fatal("duplicate Chinese username accepted", err)
	}
}

func TestOrdinaryUsersCannotEnterAdminAPIsAndOnlySeeAssignedDevices(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	admin := login.Result().Cookies()[0]
	one, _, _ := a.store.CreateDevice("authorized router")
	two, _, _ := a.store.CreateDevice("private router")
	first, _ := a.store.PutMapping(config.Mapping{Host: "one.example.com", DeviceID: one.ID, Target: "http://127.0.0.1:80", Enabled: true, AccessMode: "public"})
	second, _ := a.store.PutMapping(config.Mapping{Host: "two.example.com", DeviceID: two.ID, Target: "http://127.0.0.1:80", Enabled: true, AccessMode: "public"})
	a.traffic = traffic.New()
	a.traffic.Add(one.ID, first.ID, 12, 34)
	a.traffic.Add(two.ID, second.ID, 9876, 5432)
	createRegular(t, a, "guest", []deviceGrant{{DeviceID: one.ID}})
	cookie := regularCookie(t, a, "guest")
	for _, path := range []string{"/api/admin/state", "/api/admin/domains", "/api/admin/package-center", "/api/admin/security", "/api/admin/users", "/api/admin/mail", "/api/admin/mail/preview", "/api/admin/mail/templates", "/api/admin/notifications"} {
		requireStatus(t, authCall(a, path, "", "", cookie), 401)
	}
	result := authCall(a, "/api/member/state", "", "", cookie)
	requireStatus(t, result, 200)
	for _, forbidden := range []string{two.ID, "private router", second.Host, one.TokenHash, "tokenHash", "passwordHash", "accessRevision", "9876", "target"} {
		if strings.Contains(result.Body.String(), forbidden) {
			t.Fatal("ordinary snapshot leaked data outside its grant", forbidden)
		}
	}
	if !strings.Contains(result.Body.String(), first.Host) || !strings.Contains(result.Body.String(), `"uploadBytes":12`) {
		t.Fatal("authorized data missing")
	}
	requireStatus(t, authCall(a, "/api/member/traffic/trends?scope=devices&period=day&id="+two.ID, "", "", cookie), 403)
	result = authCall(a, "/api/member/traffic/trends?scope=mappings&period=day", "", "", cookie)
	requireStatus(t, result, 200)
	if strings.Contains(result.Body.String(), "9876") {
		t.Fatal("aggregate bypassed grants")
	}
	requireStatus(t, authCall(a, "/api/member/devices/"+two.ID+"/events", "", "", cookie), 403)
	requireStatus(t, authCall(a, "/api/member/devices/"+one.ID+"/events", "", "", cookie), 200)
	requireStatus(t, authCall(a, "/api/member/devices/"+one.ID+"/remote", `{"action":"fingerprint"}`, "", cookie), 403)
	requireStatus(t, authCall(a, "/api/admin/users", "{}", "https://evil.example", admin), 403)
	requireStatus(t, authCall(a, "/api/admin/mail", "{}", "https://evil.example", admin), 403)
	status := authCall(a, "/api/auth/status", "", "", cookie)
	if !strings.Contains(status.Body.String(), `"role":"user"`) {
		t.Fatal(status.Body.String())
	}
	requireStatus(t, authCall(a, memberPath, "", "", cookie), 200)
}

func TestOrdinaryUsersPersistHashedPasswordsAndRevocationIsScoped(t *testing.T) {
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	device, _, _ := a.store.CreateDevice("router")
	u := createRegular(t, a, "guest", []deviceGrant{{DeviceID: device.ID, SSH: true}})
	createRegular(t, a, "other", nil)
	first := regularCookie(t, a, "guest")
	second := regularCookie(t, a, "other")
	raw, _ := os.ReadFile(a.auth.path)
	if strings.Contains(string(raw), "member-test-password") {
		t.Fatal("plaintext ordinary password saved")
	}
	restored, err := openAdminManager(a.auth.path, "", "")
	if err != nil || len(restored.record.Users) != 2 {
		t.Fatal("restart lost users", err)
	}
	_, owners, err := a.auth.putUser(userInput{ID: u.ID, Username: u.Username, Enabled: true, Grants: nil})
	if err != nil {
		t.Fatal(err)
	}
	a.closeRevokedOwners(owners)
	requireStatus(t, authCall(a, "/api/member/state", "", "", first), 401)
	requireStatus(t, authCall(a, "/api/member/state", "", "", second), 200)
	cookie := regularCookie(t, a, "guest")
	result := authCall(a, "/api/member/state", "", "", cookie)
	if strings.Contains(result.Body.String(), device.ID) {
		t.Fatal("removed device grant survived relogin")
	}
	expiry := time.Now().Add(-time.Minute)
	_, owners, err = a.auth.putUser(userInput{ID: u.ID, Username: u.Username, Enabled: true, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	a.closeRevokedOwners(owners)
	requireStatus(t, authCall(a, "/api/member/state", "", "", cookie), 401)
	rawLogin, _ := json.Marshal(map[string]string{"username": "guest", "password": "member-test-password"})
	requireStatus(t, authCall(a, "/api/auth/login", string(rawLogin), "", nil), 401)
}

func TestOrdinaryMappingGrantChecksAndLogoutDoNotRevokeOtherUsers(t *testing.T) {
	f := newAccessFixture(t)
	other, _, _ := f.app.store.CreateDevice("not authorized")
	user := createRegular(t, f.app, "guest", []deviceGrant{{DeviceID: f.mapping.DeviceID}})
	createRegular(t, f.app, "other", []deviceGrant{{DeviceID: f.mapping.DeviceID}})
	issue := func(name string) *http.Cookie {
		w := httptest.NewRecorder()
		f.app.auth.issueSession(w, httptest.NewRequest("POST", "https://console.example.com/login", nil), name)
		return w.Result().Cookies()[0]
	}
	guestCookie, otherCookie := issue("guest"), issue("other")
	f.parent = guestCookie
	guestAccess := completeAccess(t, f)
	f.parent = otherCookie
	otherAccess := completeAccess(t, f)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", guestAccess), 502)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", otherAccess), 502)
	logout := httptest.NewRequest("POST", "https://console.example.com/api/auth/logout", strings.NewReader("{}"))
	logout.AddCookie(guestCookie)
	logout.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, logout)
	requireStatus(t, w, 204)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", guestAccess), 302)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", otherAccess), 502)
	guestCookie = issue("guest")
	f.parent = guestCookie
	guestAccess = completeAccess(t, f)
	_, owners, err := f.app.auth.putUser(userInput{ID: user.ID, Username: user.Username, Enabled: true, Grants: []deviceGrant{{DeviceID: other.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	f.app.closeRevokedOwners(owners)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", guestAccess), 302)
	f.parent = issue("guest")
	authorize, _ := beginAccess(t, f)
	requireStatus(t, accessCall(f.app, "GET", authorize, f.parent), 403)
}

func TestFilesOnlyGrantCannotOpenTerminalAndRevocationClosesSession(t *testing.T) {
	fixture := newRemoteFixture(t)
	u := createRegular(t, fixture.app, "fileguest", []deviceGrant{{DeviceID: fixture.device, Files: true}})
	cookie := regularCookie(t, fixture.app, "fileguest")
	path := "/api/member/devices/" + fixture.device + "/remote"
	requireStatus(t, remoteTestCall(fixture.app, cookie, path, map[string]any{"action": "fingerprint", "view": "terminal"}), 403)
	probe := remoteTestCall(fixture.app, cookie, path, map[string]any{"action": "fingerprint", "view": "files", "host": fixture.host, "port": fixture.port})
	requireStatus(t, probe, 200)
	connected := remoteTestCall(fixture.app, cookie, path, map[string]any{"action": "connect", "view": "files", "host": fixture.host, "port": fixture.port, "username": testssh.Username, "password": testssh.Password, "fingerprint": fixture.ssh.Fingerprint})
	requireStatus(t, connected, 200)
	var session struct {
		ID string `json:"id"`
	}
	json.Unmarshal(connected.Body.Bytes(), &session)
	base := "/api/member/remote/" + session.ID
	requireStatus(t, remoteTestCall(fixture.app, cookie, base+"/files", map[string]any{"action": "list"}), 200)
	// A terminal request checks the grant before WebSocket upgrade or ticket parsing.
	requireStatus(t, remoteTestCall(fixture.app, cookie, base+"/terminal", nil), 403)
	connected = remoteTestCall(fixture.app, cookie, path, map[string]any{"action": "connect", "view": "files", "host": fixture.host, "port": fixture.port, "username": testssh.Username, "password": testssh.Password, "fingerprint": fixture.ssh.Fingerprint})
	requireStatus(t, connected, 200)
	json.Unmarshal(connected.Body.Bytes(), &session)
	_, owners, err := fixture.app.auth.putUser(userInput{ID: u.ID, Username: u.Username, Enabled: false, Grants: u.Grants})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.closeRevokedOwners(owners)
	fixture.app.remote.mu.Lock()
	_, exists := fixture.app.remote.sessions[session.ID]
	fixture.app.remote.mu.Unlock()
	if exists {
		t.Fatal("revocation left the SFTP session alive")
	}
	if fixture.app.auth.ownerAllows(sha256.Sum256([]byte(cookie.Value)), fixture.device, "files") {
		t.Fatal("revoked owner still authorized")
	}
}

func TestUserWriteFailurePreservesCredentialsAndSessions(t *testing.T) {
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	u := createRegular(t, a, "guest", nil)
	cookie := regularCookie(t, a, "guest")
	original := a.auth.path
	a.auth.path = original + "/blocked" // parent is the existing account file
	_, owners, err := a.auth.putUser(userInput{ID: u.ID, Username: u.Username, Enabled: false})
	a.auth.path = original
	if err == nil || len(owners) != 0 {
		t.Fatal("failed save accepted")
	}
	requireStatus(t, authCall(a, "/api/member/state", "", "", cookie), 200)
}

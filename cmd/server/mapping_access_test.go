package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
)

type accessFixture struct {
	app     *app
	mapping config.Mapping
	parent  *http.Cookie
}

func newAccessFixture(t *testing.T) accessFixture {
	t.Helper()
	a := freshAuthApp(t)
	requireStatus(t, authCall(a, "/api/auth/setup", setupBody, "", nil), 200)
	a.consoleHost = "console.example.com"
	d, _, err := a.store.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: d.ID, Target: "http://127.0.0.1:80", Enabled: true, AccessMode: "admin", AccessConsoleURL: "https://console.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.auth.issueSession(w, httptest.NewRequest("POST", "https://console.example.com/login", nil), "owner")
	return accessFixture{a, m, w.Result().Cookies()[0]}
}
func accessCall(a *app, method, address string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, address, nil)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func beginAccess(t *testing.T, f accessFixture) (string, *http.Cookie) {
	t.Helper()
	w := accessCall(f.app, "GET", "https://router.example.com/private?tab=settings")
	requireStatus(t, w, 302)
	w = accessCall(f.app, "GET", w.Header().Get("Location"))
	requireStatus(t, w, 302)
	state := w.Result().Cookies()[0]
	if !state.HttpOnly || !state.Secure || state.Domain != "" || state.SameSite != http.SameSiteLaxMode {
		t.Fatalf("Unsafe state cookie: %+v", state)
	}
	return w.Header().Get("Location"), state
}
func completeAccess(t *testing.T, f accessFixture) *http.Cookie {
	t.Helper()
	authorize, state := beginAccess(t, f)
	w := accessCall(f.app, "GET", authorize, f.parent)
	requireStatus(t, w, 302)
	if strings.Contains(w.Header().Get("Location"), f.parent.Value) {
		t.Fatal("Administrator token leaked")
	}
	w = accessCall(f.app, "GET", w.Header().Get("Location"), state)
	requireStatus(t, w, 302)
	if w.Header().Get("Location") != "https://router.example.com/private?tab=settings" {
		t.Fatal(w.Header())
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == mappingAccessCookie {
			return cookie
		}
	}
	t.Fatal("Mapping session not issued")
	return nil
}
func TestMappingAccessProtectsAllPathsAndRequiresAnActualMappingSession(t *testing.T) {
	f := newAccessFixture(t)
	for _, path := range []string{"/", "/api/admin/state", "/api/auth/status", "/cgi-bin/luci/"} {
		w := accessCall(f.app, "GET", "https://router.example.com"+path)
		requireStatus(t, w, 302)
	}
	// The console token alone and a forged mapping cookie do not authorize a mapping.
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", f.parent), 302)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", &http.Cookie{Name: mappingAccessCookie, Value: strings.Repeat("a", 64)}), 302)
	w := accessCall(f.app, "POST", "https://router.example.com/api/reboot")
	requireStatus(t, w, 401)
	if !strings.HasPrefix(w.Header().Get("Location"), "https://router.example.com/") {
		t.Fatal(w.Header())
	}
	w = accessCall(f.app, "GET", "https://router.example.com:8443/")
	requireStatus(t, w, 403)
	visitor := completeAccess(t, f)
	if !visitor.HttpOnly || !visitor.Secure || visitor.Domain != "" || visitor.Name == f.parent.Name {
		t.Fatal("Visitor cookie is not isolated")
	}
	// Reaching the offline target proves authorization; /api/admin/state must not
	// accidentally return the RemoteGate management API on a mapped domain.
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/api/admin/state", visitor), 502)
}
func TestMappingAccessLoginContinuationAndBrowserBoundOneTimeCode(t *testing.T) {
	f := newAccessFixture(t)
	authorize, state := beginAccess(t, f)
	w := accessCall(f.app, "GET", authorize)
	requireStatus(t, w, 302)
	login, _ := url.Parse(w.Header().Get("Location"))
	if login.Host != "console.example.com" || login.Query().Get("access_request") == "" {
		t.Fatal(login)
	}
	// An arbitrary host cannot redeem a challenge or obtain a login redirect.
	wrong := strings.Replace(authorize, "console.example.com", "evil.example.com", 1)
	requireStatus(t, accessCall(f.app, "GET", wrong, f.parent), 403)
	w = accessCall(f.app, "GET", authorize, f.parent)
	requireStatus(t, w, 302)
	callback := w.Header().Get("Location")
	requireStatus(t, accessCall(f.app, "GET", callback), 403)
	fake := *state
	fake.Value = strings.Repeat("b", 64)
	requireStatus(t, accessCall(f.app, "GET", callback, &fake), 403)
	w = accessCall(f.app, "GET", callback, state)
	requireStatus(t, w, 302)
	requireStatus(t, accessCall(f.app, "GET", callback, state), 403)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("Callback must not be cached or leak its ticket")
	}
}
func TestMappingSessionRevokesOnLogoutExpiryAndMappingChange(t *testing.T) {
	for _, change := range []string{"logout", "logout-after-second-login", "expiry", "mapping", "permissions", "password"} {
		t.Run(change, func(t *testing.T) {
			f := newAccessFixture(t)
			visitor := completeAccess(t, f)
			switch change {
			case "logout":
				requireStatus(t, authCall(f.app, "/api/auth/logout", `{}`, "", f.parent), 204)
			case "logout-after-second-login":
				w := httptest.NewRecorder()
				f.app.auth.issueSession(w, httptest.NewRequest("POST", "https://console.example.com/login", nil), "owner")
				requireStatus(t, authCall(f.app, "/api/auth/logout", `{}`, "", w.Result().Cookies()[0]), 204)
			case "expiry":
				key := sha256.Sum256([]byte(f.parent.Value))
				f.app.auth.mu.Lock()
				s := f.app.auth.sessions[key]
				s.expires = time.Now().Add(-time.Second)
				f.app.auth.sessions[key] = s
				f.app.auth.mu.Unlock()
			case "password":
				if err := f.app.auth.updateSecurity("test-password-123", "", "updated-password-123"); err != nil {
					t.Fatal(err)
				}
			case "mapping":
				m := f.mapping
				m.Target = "http://127.0.0.1:8080"
				if _, err := f.app.store.PutMapping(m); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				m := f.mapping
				m.AccessMode = "public"
				m, err := f.app.store.PutMapping(m)
				if err != nil {
					t.Fatal(err)
				}
				m.AccessMode = "admin"
				m.AccessConsoleURL = "https://console.example.com"
				if _, err = f.app.store.PutMapping(m); err != nil {
					t.Fatal(err)
				}
			}
			requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", visitor), 302)
		})
	}
}
func TestMappingAccessRejectsExpiredOrChangedChallenges(t *testing.T) {
	for _, change := range []string{"challenge", "code", "disable", "host", "target"} {
		t.Run(change, func(t *testing.T) {
			f := newAccessFixture(t)
			authorize, state := beginAccess(t, f)
			w := accessCall(f.app, "GET", authorize, f.parent)
			requireStatus(t, w, 302)
			callback := w.Header().Get("Location")
			manager := f.app.mappingAccessManager()
			u, _ := url.Parse(callback)
			switch change {
			case "challenge":
				manager.mu.Lock()
				c := manager.challenges[u.Query().Get("request")]
				c.Expires = time.Now().Add(-time.Second)
				manager.challenges[u.Query().Get("request")] = c
				manager.mu.Unlock()
			case "code":
				manager.mu.Lock()
				c := manager.codes[u.Query().Get("code")]
				c.Expires = time.Now().Add(-time.Second)
				manager.codes[u.Query().Get("code")] = c
				manager.mu.Unlock()
			default:
				m := f.mapping
				if change == "disable" {
					m.Enabled = false
				}
				if change == "host" {
					m.Host = "new.example.com"
				}
				if change == "target" {
					m.Target = "http://127.0.0.1:8080"
				}
				if _, err := f.app.store.PutMapping(m); err != nil {
					t.Fatal(err)
				}
			}
			w = accessCall(f.app, "GET", callback, state)
			if w.Code == 302 || len(w.Result().Cookies()) > 0 {
				t.Fatal("Expired or changed authorization accepted", w.Code)
			}
		})
	}
}
func TestMappingAccessScopesSessionsAndRejectsCrossOriginWrites(t *testing.T) {
	f := newAccessFixture(t)
	visitor := completeAccess(t, f)
	m := f.mapping
	m.ID = ""
	m.Host = "other.example.com"
	if _, err := f.app.store.PutMapping(m); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, accessCall(f.app, "GET", "https://other.example.com/", visitor), 302)
	r := httptest.NewRequest("POST", "https://router.example.com/reboot", nil)
	r.AddCookie(visitor)
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, r)
	requireStatus(t, w, 403)
	for _, bad := range []string{"//evil.example.com", "https://evil.example.com/", "/\\evil", "/%5cevil", "/path%0d%0aheader", "/.remotegate/access/callback", "/a#fragment"} {
		if _, err := safeAccessReturn(bad); err == nil {
			t.Fatalf("Unsafe redirect accepted: %s", bad)
		}
	}
}
func TestMappingProxyNeverForwardsGatewayCredentials(t *testing.T) {
	r := httptest.NewRequest("GET", "https://router.example.com/", nil)
	r.AddCookie(&http.Cookie{Name: adminCookie, Value: "administrator"})
	r.AddCookie(&http.Cookie{Name: mappingAccessCookie, Value: "gateway"})
	r.AddCookie(&http.Cookie{Name: mappingStateCookie, Value: "nonce"})
	r.AddCookie(&http.Cookie{Name: "sysauth", Value: "luci-session"})
	r.Header.Set("Authorization", "Basic app-credentials")
	h := http.Header(mappingProxyHeaders(r))
	if h.Get("Cookie") != "sysauth=luci-session" || h.Get("Authorization") != "Basic app-credentials" {
		t.Fatal(h)
	}
	for _, name := range []string{adminCookie, httpAdminCookie, mappingAccessCookie, mappingStateCookie, mappingAccessHTTPCookie, mappingStateHTTPCookie} {
		if mappingProxyCookie(name + "=evil; Path=/; Secure") {
			t.Fatal("Reserved response cookie accepted", name)
		}
	}
	if !mappingProxyCookie("sysauth=new-session; Path=/; HttpOnly") {
		t.Fatal("Application login cookie removed")
	}
}

func TestMappingAccessAllowsLocalLoginFormsAndRejectsForeignOrigins(t *testing.T) {
	f := newAccessFixture(t)
	visitor := completeAccess(t, f)
	for _, origin := range []string{"https://router.example.com", "https://router.example.com:443", "https://ROUTER.example.com", "http://router.example.com", "https://router.example.com:8443", "https://evil.example.com", "null"} {
		t.Run(origin, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://router.example.com/cgi-bin/luci/", strings.NewReader("luci_username=root&luci_password=fixture-only"))
			r.AddCookie(visitor)
			r.Header.Set("Origin", origin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			w := httptest.NewRecorder()
			f.app.routes().ServeHTTP(w, r)
			want := http.StatusForbidden
			if origin == "https://router.example.com" || origin == "https://router.example.com:443" || origin == "https://ROUTER.example.com" {
				want = http.StatusBadGateway // Authorization passed; fixture device is offline.
			}
			requireStatus(t, w, want)
		})
	}
	r := httptest.NewRequest("POST", "https://router.example.com/cgi-bin/luci/", nil)
	r.AddCookie(visitor)
	r.Header.Set("Origin", "https://router.example.com")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, r)
	requireStatus(t, w, http.StatusForbidden)
}

func TestMappingAccessAuthorizesRealTunnelAndKeepsApplicationLogin(t *testing.T) {
	f := newAccessFixture(t)
	d, token, err := f.app.store.CreateDevice("connected")
	if err != nil {
		t.Fatal(err)
	}
	f.mapping.DeviceID = d.ID
	f.mapping, err = f.app.store.PutMapping(f.mapping)
	if err != nil {
		t.Fatal(err)
	}
	ts := agentTestServer(t, f.app)
	defer ts.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/agent/connect?device_id="+d.ID+"&token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requests := make(chan protocol.Message, 1)
	go func() {
		var request protocol.Message
		if conn.ReadJSON(&request) == nil {
			requests <- request
			_ = conn.WriteJSON(protocol.Message{Type: "response", ID: request.ID, Status: 200, Headers: map[string][]string{"Set-Cookie": {"sysauth=renewed; Path=/; HttpOnly", mappingAccessCookie + "=forged; Path=/; Secure"}, "Cache-Control": {"public, max-age=3600"}}, Body: []byte("private router page")})
		}
	}()
	visitor := completeAccess(t, f)
	w := accessCall(f.app, "GET", "https://router.example.com/api/app?view=status", visitor, f.parent, &http.Cookie{Name: "sysauth", Value: "router-login"})
	requireStatus(t, w, 200)
	if w.Body.String() != "private router page" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w)
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("Application pages must preserve the Origin of local login forms")
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != "sysauth" {
		t.Fatal(cookies)
	}
	select {
	case request := <-requests:
		if request.Path != "/api/app?view=status" || http.Header(request.Headers).Get("Cookie") != "sysauth=router-login" {
			t.Fatal(request)
		}
	case <-time.After(time.Second):
		t.Fatal("Authorized request did not reach the tunnel")
	}
}

func TestMappingAccessPublicOptOutAndHTTPSUpgrade(t *testing.T) {
	f := newAccessFixture(t)
	w := accessCall(f.app, "GET", "http://router.example.com/private?tab=settings")
	requireStatus(t, w, 307)
	if w.Header().Get("Location") != "https://router.example.com/private?tab=settings" {
		t.Fatal(w.Header())
	}
	requireStatus(t, accessCall(f.app, "POST", "http://router.example.com/reboot"), 403)
	f.mapping.AccessMode = "public"
	if _, err := f.app.store.PutMapping(f.mapping); err != nil {
		t.Fatal(err)
	}
	// A public mapping reaches its target without either administrator or visitor cookies.
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/"), 502)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/api/admin/state"), 502)
}

func TestAnonymousLogoutCannotRevokeOtherVisitors(t *testing.T) {
	f := newAccessFixture(t)
	visitor := completeAccess(t, f)
	requireStatus(t, authCall(f.app, "/api/auth/logout", `{}`, "", nil), 204)
	requireStatus(t, accessCall(f.app, "GET", "https://router.example.com/", visitor), 502)
}

func TestPublicMappingCannotReplaceTheConsoleHost(t *testing.T) {
	f := newAccessFixture(t)
	r := httptest.NewRequest("POST", "https://console.example.com/api/admin/mappings", strings.NewReader(`{"host":"console.example.com","accessMode":"public"}`))
	r.AddCookie(f.parent)
	w := httptest.NewRecorder()
	f.app.routes().ServeHTTP(w, r)
	requireStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "独立子域名") {
		t.Fatal(w.Body.String())
	}
}

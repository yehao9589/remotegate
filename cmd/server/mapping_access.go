package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
)

const mappingAccessPath = "/.remotegate/access/"
const mappingAccessCookie = "__Host-remotegate_access"
const mappingAccessHTTPCookie = "remotegate_access_http"
const mappingStateCookie = "__Host-remotegate_access_state"
const mappingStateHTTPCookie = "remotegate_access_state_http"
const mappingAccessLimit = 4096

type accessChallenge struct {
	MappingID, Scope, ReturnPath string
	Verifier                     [32]byte
	Expires                      time.Time
}
type accessCode struct {
	Request string
	Owner   [32]byte
	Expires time.Time
}
type mappingSession struct {
	Scope   string
	Owner   [32]byte
	Expires time.Time
}
type mappingAccessManager struct {
	mu         sync.Mutex
	challenges map[string]accessChallenge
	codes      map[string]accessCode
	sessions   map[[32]byte]mappingSession
}

func (a *app) mappingAccessManager() *mappingAccessManager {
	a.mappingMu.Lock()
	defer a.mappingMu.Unlock()
	if a.mappingAccess == nil {
		a.mappingAccess = &mappingAccessManager{challenges: make(map[string]accessChallenge), codes: make(map[string]accessCode), sessions: make(map[[32]byte]mappingSession)}
	}
	return a.mappingAccess
}

// There is one administrator account. Clear its mapping grants on logout,
// including grants issued before a later login replaced the browser's cookie.
// Other browsers with a live console session can authorize again normally.
func (a *app) revokeMappingAccess() {
	a.mappingMu.Lock()
	manager := a.mappingAccess
	a.mappingMu.Unlock()
	if manager == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	clear(manager.challenges)
	clear(manager.codes)
	clear(manager.sessions)
}
func accessToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
func validAccessToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (m *mappingAccessManager) pruneLocked(now time.Time) {
	for key, item := range m.challenges {
		if !now.Before(item.Expires) {
			delete(m.challenges, key)
		}
	}
	for key, item := range m.codes {
		if !now.Before(item.Expires) {
			delete(m.codes, key)
		}
	}
	for key, item := range m.sessions {
		if !now.Before(item.Expires) {
			delete(m.sessions, key)
		}
	}
}

func (a *app) configuredAccessConsole() (*url.URL, error) {
	if a.publicURL != nil {
		return validatedAccessOrigin(a.publicURL.String())
	}
	if a.consoleHost != "" {
		return validatedAccessOrigin("https://" + a.consoleHost)
	}
	return nil, nil
}
func validatedAccessOrigin(value string) (*url.URL, error) {
	u, err := parsePublicURL(value)
	if err != nil {
		return nil, err
	}
	if err = config.ValidateAccessConsole(u.String()); err != nil {
		return nil, err
	}
	return u, nil
}
func (a *app) accessConsoleFromAdmin(r *http.Request) (*url.URL, error) {
	if configured, err := a.configuredAccessConsole(); configured != nil || err != nil {
		return configured, err
	}
	scheme := "http"
	if secureAuthRequest(r) {
		scheme = "https"
	}
	u, err := validatedAccessOrigin(scheme + "://" + r.Host)
	if err != nil {
		return nil, err
	}
	if _, mapped := a.store.MappingForHost(u.Host); mapped {
		return nil, errors.New("请从后台域名配置访问验证")
	}
	return u, nil
}
func (a *app) accessConsole(m config.Mapping) (*url.URL, error) {
	u, err := a.configuredAccessConsole()
	if err != nil {
		return nil, err
	}
	if u == nil && m.AccessConsoleURL != "" {
		u, err = validatedAccessOrigin(m.AccessConsoleURL)
	}
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("请管理员先通过 HTTPS 打开并登录后台，完成认证地址绑定")
	}
	if strings.EqualFold(u.Hostname(), m.Host) {
		return nil, errors.New("映射域名与后台认证域名不能相同")
	}
	return u, nil
}
func mappingOrigin(m config.Mapping) (*url.URL, error) {
	if m.PublicScheme != "https" && !(m.PublicScheme == "http" && config.LocalMappingHost(m.Host)) {
		return nil, errors.New("此受保护映射需要 HTTPS 公网入口")
	}
	host := m.Host
	if m.PublicPort != 0 {
		host = net.JoinHostPort(host, strconv.Itoa(m.PublicPort))
	}
	return parsePublicURL(m.PublicScheme + "://" + host)
}
func matchingAccessHost(r *http.Request, origin *url.URL) bool {
	u, err := parsePublicURL(origin.Scheme + "://" + r.Host)
	return err == nil && u.Host == origin.Host
}
func accessTransport(w http.ResponseWriter, r *http.Request, origin *url.URL) bool {
	if origin.Scheme != "https" || secureAuthRequest(r) {
		return true
	}
	accessNoStore(w)
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, origin.String()+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	} else {
		accessError(w, http.StatusForbidden, "受保护映射须通过 HTTPS 访问，请重新打开 HTTPS 地址", origin.String()+"/")
	}
	return false
}
func accessScope(m config.Mapping, console *url.URL) string {
	raw, _ := json.Marshal([]any{m.ID, m.Host, m.Target, m.DeviceID, m.PublicScheme, m.PublicPort, m.AccessMode, m.AccessRevision, console.String()})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (a *app) accessMapping(id string) (config.Mapping, bool) {
	for _, m := range a.store.Snapshot().Mappings {
		if m.ID == id && m.Enabled && m.AccessMode != "public" {
			return m, true
		}
	}
	return config.Mapping{}, false
}
func (a *app) accessOwnerValid(owner [32]byte) (time.Time, bool) {
	if a.auth == nil {
		return time.Time{}, false
	}
	a.auth.mu.Lock()
	defer a.auth.mu.Unlock()
	s, exists := a.auth.sessions[owner]
	return s.expires, exists && time.Now().Before(s.expires)
}
func accessCookieNames(m config.Mapping) (string, string) {
	if m.PublicScheme == "https" {
		return mappingAccessCookie, mappingStateCookie
	}
	return mappingAccessHTTPCookie, mappingStateHTTPCookie
}
func setMappingCookie(w http.ResponseWriter, m config.Mapping, name, value string, expires time.Time) {
	seconds := int(time.Until(expires).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: m.PublicScheme == "https", SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: seconds})
}
func accessNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func accessError(w http.ResponseWriter, code int, message, retry string) {
	accessNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>RemoteGate · 访问验证</title><style>body{margin:0;background:#f4f7fc;color:#283f60;font:16px/1.8 system-ui,sans-serif}main{max-width:480px;margin:12vh auto;padding:32px;background:white;border:1px solid #dfe6f4;border-radius:20px}small{color:#6480bf}h1{font-size:24px}p{color:#7386a3}a{display:inline-block;padding:10px 20px;background:#5065df;color:white;text-decoration:none;border-radius:10px}@media(max-width:550px){main{margin:10vh 18px;padding:25px}}</style><main><small>RemoteGate · 身份验证</small><h1>需要验证后才能访问</h1><p>%s</p>`, template.HTMLEscapeString(message))
	if retry != "" {
		_, _ = fmt.Fprintf(w, `<a href="%s">重新验证</a>`, template.HTMLEscapeString(retry))
	}
	_, _ = fmt.Fprint(w, `</main></html>`)
}
func safeAccessReturn(value string) (string, error) {
	if value == "" {
		return "/", nil
	}
	if len(value) > 4096 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\x00\r\n") {
		return "", errors.New("访问地址无效")
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.HasPrefix(u.Path, mappingAccessPath) {
		return "", errors.New("访问地址无效")
	}
	return value, nil
}

func (a *app) mappingAccessHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, mappingAccessPath) {
			a.mappingAccessHTTP(w, r)
			return
		}
		if m, found := a.store.MappingForHost(r.Host); found {
			if m.AccessMode != "public" && !a.requireMappingAccess(w, r, m) {
				return
			}
			// Mapping applications own their /api routes; they never reach console APIs.
			a.root(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *app) requireMappingAccess(w http.ResponseWriter, r *http.Request, m config.Mapping) bool {
	origin, err := mappingOrigin(m)
	if err != nil {
		accessError(w, 503, err.Error(), "")
		return false
	}
	console, err := a.accessConsole(m)
	if err != nil {
		accessError(w, 503, err.Error(), "")
		return false
	}
	if !matchingAccessHost(r, origin) {
		accessError(w, 403, "访问端口与映射配置不一致，请使用后台显示的完整地址", origin.String()+"/")
		return false
	}
	if !accessTransport(w, r, origin) {
		return false
	}
	if a.mappingSessionValid(r, m, console) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if supplied := r.Header.Get("Origin"); supplied != "" {
				actual, err := parsePublicURL(supplied)
				if err != nil || actual.String() != origin.String() {
					accessError(w, 403, "请求来源与受保护映射不一致", origin.String()+"/")
					return false
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				accessError(w, 403, "已拒绝跨站操作请求", origin.String()+"/")
				return false
			}
		}
		return true
	}
	path, err := safeAccessReturn(r.URL.RequestURI())
	if err != nil {
		accessError(w, 400, err.Error(), origin.String()+"/")
		return false
	}
	login := origin.String() + mappingAccessPath + "start?" + url.Values{"return": {path}}.Encode()
	accessNoStore(w)
	if r.Method == http.MethodGet && !strings.Contains(r.Header.Get("Accept"), "application/json") && (r.Header.Get("Sec-Fetch-Dest") == "" || r.Header.Get("Sec-Fetch-Dest") == "document") {
		http.Redirect(w, r, login, http.StatusFound)
	} else {
		w.Header().Set("Location", login)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "RemoteGate: 需要先通过后台登录验证；验证后请重新发起请求")
	}
	return false
}
func (a *app) mappingSessionValid(r *http.Request, m config.Mapping, console *url.URL) bool {
	name, _ := accessCookieNames(m)
	cookie, err := r.Cookie(name)
	if err != nil || !validAccessToken(cookie.Value) {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	manager := a.mappingAccessManager()
	manager.mu.Lock()
	session, ok := manager.sessions[key]
	manager.mu.Unlock()
	if !ok || time.Now().After(session.Expires) || session.Scope != accessScope(m, console) {
		return false
	}
	_, valid := a.accessOwnerValid(session.Owner)
	if !valid {
		manager.mu.Lock()
		delete(manager.sessions, key)
		manager.mu.Unlock()
	}
	return valid
}

func (a *app) mappingAccessHTTP(w http.ResponseWriter, r *http.Request) {
	accessNoStore(w)
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == mappingAccessPath+"authorize" {
		a.authorizeMappingAccess(w, r)
		return
	}
	m, found := a.store.MappingForHost(r.Host)
	if !found || m.AccessMode == "public" {
		http.NotFound(w, r)
		return
	}
	origin, err := mappingOrigin(m)
	if err != nil {
		accessError(w, 503, err.Error(), "")
		return
	}
	console, err := a.accessConsole(m)
	if err != nil {
		accessError(w, 503, err.Error(), "")
		return
	}
	if !matchingAccessHost(r, origin) {
		accessError(w, 403, "访问入口不匹配", origin.String()+"/")
		return
	}
	if !accessTransport(w, r, origin) {
		return
	}
	switch r.URL.Path {
	case mappingAccessPath + "start":
		a.startMappingAccess(w, r, m, origin, console)
	case mappingAccessPath + "callback":
		a.completeMappingAccess(w, r, m, origin, console)
	default:
		http.NotFound(w, r)
	}
}
func (a *app) startMappingAccess(w http.ResponseWriter, r *http.Request, m config.Mapping, origin, console *url.URL) {
	path, err := safeAccessReturn(r.URL.Query().Get("return"))
	if err != nil {
		accessError(w, 400, err.Error(), origin.String()+"/")
		return
	}
	id, verifier := accessToken(), accessToken()
	if id == "" || verifier == "" {
		accessError(w, 503, "暂时无法发起验证，请稍后重试", origin.String()+"/")
		return
	}
	expires := time.Now().Add(5 * time.Minute)
	manager := a.mappingAccessManager()
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	if len(manager.challenges) >= mappingAccessLimit {
		manager.mu.Unlock()
		accessError(w, 429, "验证请求过多，请稍后重试", origin.String()+"/")
		return
	}
	manager.challenges[id] = accessChallenge{MappingID: m.ID, Scope: accessScope(m, console), ReturnPath: path, Verifier: sha256.Sum256([]byte(verifier)), Expires: expires}
	manager.mu.Unlock()
	_, name := accessCookieNames(m)
	setMappingCookie(w, m, name, verifier, expires)
	http.Redirect(w, r, console.String()+mappingAccessPath+"authorize?request="+id, http.StatusFound)
}
func (a *app) authorizeMappingAccess(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("request")
	manager := a.mappingAccessManager()
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	challenge, found := manager.challenges[id]
	manager.mu.Unlock()
	if !validAccessToken(id) || !found {
		accessError(w, 403, "验证请求已过期或无效，请从原访问地址重新打开", "")
		return
	}
	m, found := a.accessMapping(challenge.MappingID)
	if !found {
		accessError(w, 403, "映射已停用或访问设置已更改", "")
		return
	}
	console, err := a.accessConsole(m)
	if err != nil || !matchingAccessHost(r, console) || challenge.Scope != accessScope(m, console) {
		accessError(w, 403, "认证入口或映射设置不匹配", "")
		return
	}
	if !accessTransport(w, r, console) {
		return
	}
	origin, err := mappingOrigin(m)
	if err != nil {
		accessError(w, 403, "映射入口不可用", "")
		return
	}
	owner, valid := a.remoteOwner(r)
	if !valid {
		if a.auth == nil || !a.auth.initialized() {
			accessError(w, 503, "请管理员先完成后台初始化", "")
			return
		}
		http.Redirect(w, r, console.String()+a.auth.entryPath()+"?access_request="+id, http.StatusFound)
		return
	}
	code := accessToken()
	if code == "" {
		accessError(w, 503, "暂时无法完成验证", origin.String()+"/")
		return
	}
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	if len(manager.codes) >= mappingAccessLimit {
		manager.mu.Unlock()
		accessError(w, 429, "验证请求过多，请稍后重试", origin.String()+"/")
		return
	}
	manager.codes[code] = accessCode{Request: id, Owner: owner, Expires: time.Now().Add(time.Minute)}
	manager.mu.Unlock()
	http.Redirect(w, r, origin.String()+mappingAccessPath+"callback?"+url.Values{"request": {id}, "code": {code}}.Encode(), http.StatusFound)
}
func (a *app) completeMappingAccess(w http.ResponseWriter, r *http.Request, m config.Mapping, origin, console *url.URL) {
	id, code := r.URL.Query().Get("request"), r.URL.Query().Get("code")
	_, stateName := accessCookieNames(m)
	cookie, err := r.Cookie(stateName)
	if !validAccessToken(id) || !validAccessToken(code) || err != nil || !validAccessToken(cookie.Value) {
		accessError(w, 403, "浏览器验证状态缺失或失效，请重新验证", origin.String()+"/")
		return
	}
	manager := a.mappingAccessManager()
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	challenge, challengeOK := manager.challenges[id]
	grant, codeOK := manager.codes[code]
	verifier := sha256.Sum256([]byte(cookie.Value))
	if !challengeOK || !codeOK || grant.Request != id || challenge.MappingID != m.ID || challenge.Scope != accessScope(m, console) || subtle.ConstantTimeCompare(verifier[:], challenge.Verifier[:]) != 1 {
		manager.mu.Unlock()
		accessError(w, 403, "验证凭证无效、已使用或属于其他浏览器", origin.String()+"/")
		return
	}
	manager.mu.Unlock()
	expires, valid := a.accessOwnerValid(grant.Owner)
	if !valid {
		accessError(w, 403, "后台登录已失效，请重新登录", origin.String()+"/")
		return
	}
	token := accessToken()
	if token == "" {
		accessError(w, 503, "暂时无法完成验证", origin.String()+"/")
		return
	}
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	// Consume both values atomically: concurrent callbacks cannot reuse a grant.
	if _, ok := manager.challenges[id]; !ok {
		manager.mu.Unlock()
		accessError(w, 403, "验证凭证已使用", origin.String()+"/")
		return
	}
	if _, ok := manager.codes[code]; !ok {
		manager.mu.Unlock()
		accessError(w, 403, "验证凭证已使用", origin.String()+"/")
		return
	}
	if len(manager.sessions) >= mappingAccessLimit {
		manager.mu.Unlock()
		accessError(w, 429, "访问会话已达上限，请稍后再试", origin.String()+"/")
		return
	}
	delete(manager.challenges, id)
	delete(manager.codes, code)
	manager.sessions[sha256.Sum256([]byte(token))] = mappingSession{Scope: challenge.Scope, Owner: grant.Owner, Expires: expires}
	manager.mu.Unlock()
	name, _ := accessCookieNames(m)
	setMappingCookie(w, m, name, token, expires)
	http.SetCookie(w, &http.Cookie{Name: stateName, Value: "", Path: "/", HttpOnly: true, Secure: m.PublicScheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, origin.String()+challenge.ReturnPath, http.StatusFound)
}

func reservedMappingCookie(name string) bool {
	return name == adminCookie || name == httpAdminCookie || name == mappingAccessCookie || name == mappingAccessHTTPCookie || name == mappingStateCookie || name == mappingStateHTTPCookie
}
func mappingProxyHeaders(r *http.Request) map[string][]string {
	header := r.Header.Clone()
	header.Del("Cookie")
	copyRequest := &http.Request{Header: header}
	for _, cookie := range r.Cookies() {
		if !reservedMappingCookie(cookie.Name) {
			copyRequest.AddCookie(cookie)
		}
	}
	return protocol.FilterHeaders(header)
}
func mappingProxyCookie(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	return ok && !reservedMappingCookie(strings.TrimSpace(name))
}

package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/local/remotegate/internal/buildinfo"
	"golang.org/x/crypto/bcrypt"
)

const adminCookie = "remotegate_session"
const sessionLifetime = 12 * time.Hour

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,31}$`)
var errAdminExists = errors.New("管理员已经配置，不能重复初始化")

type adminRecord struct {
	Version      int       `json:"version"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
}
type adminSession struct {
	user    string
	expires time.Time
}
type loginLimit struct {
	failures int
	until    time.Time
}
type adminManager struct {
	mu       sync.Mutex
	path     string
	record   *adminRecord
	sessions map[[32]byte]adminSession
	limits   map[string]loginLimit
}

func openAdminManager(path, user, pass string) (*adminManager, error) {
	m := &adminManager{path: path, sessions: make(map[[32]byte]adminSession), limits: make(map[string]loginLimit)}
	raw, err := os.ReadFile(path)
	if err == nil {
		var record adminRecord
		if json.Unmarshal(raw, &record) != nil || record.Version != 1 || !usernamePattern.MatchString(record.Username) {
			return nil, errors.New("管理员配置文件损坏，请恢复 data/admin.json 备份")
		}
		cost, e := bcrypt.Cost([]byte(record.PasswordHash))
		if e != nil || cost < 10 || cost > 14 {
			return nil, errors.New("管理员密码哈希无效")
		}
		m.record = &record
		return m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if (user == "") != (pass == "") {
		return nil, errors.New("兼容旧部署时 ADMIN_USERNAME 与 ADMIN_PASSWORD 必须同时填写；首次网页安装请同时留空")
	}
	if user != "" {
		if !usernamePattern.MatchString(user) {
			return nil, errors.New("管理员账号需为 3–32 位字母、数字、下划线、点或短横线")
		}
		if err = m.create(user, pass, false); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func (m *adminManager) initialized() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.record != nil }
func validateAdminInput(user, pass string) error {
	if !usernamePattern.MatchString(user) {
		return errors.New("账号需为 3–32 位字母、数字、下划线、点或短横线，首位为字母或数字")
	}
	if utf8.RuneCountInString(pass) < 8 || strings.TrimSpace(pass) == "" {
		return errors.New("密码至少需要 8 个字符")
	}
	if len(pass) > 72 {
		return errors.New("密码过长，请控制在 72 字节以内")
	}
	return nil
}
func (m *adminManager) create(user, pass string, validate bool) error {
	if validate {
		if err := validateAdminInput(user, pass); err != nil {
			return err
		}
	}
	if len(pass) > 72 {
		return errors.New("密码过长，请控制在 72 字节以内")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record != nil {
		return errAdminExists
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	record := adminRecord{Version: 1, Username: user, PasswordHash: string(hash), CreatedAt: time.Now().UTC()}
	if err = os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	// Publish a complete file without overwriting an existing administrator.
	temp, err := os.CreateTemp(filepath.Dir(m.path), ".admin-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(raw)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(temp.Name(), m.path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errAdminExists
		}
		return err
	}
	m.record = &record
	return nil
}
func (m *adminManager) verify(user, pass string) bool {
	m.mu.Lock()
	record := m.record
	m.mu.Unlock()
	if record == nil {
		return false
	}
	match := subtle.ConstantTimeCompare([]byte(user), []byte(record.Username)) == 1
	valid := bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(pass)) == nil
	return match && valid
}
func (m *adminManager) sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sha256.Sum256([]byte(c.Value))
	s, ok := m.sessions[key]
	if !ok || time.Now().After(s.expires) {
		delete(m.sessions, key)
		return "", false
	}
	return s.user, true
}
func (m *adminManager) issueSession(w http.ResponseWriter, r *http.Request, user string) {
	token := makeID() + makeID() + makeID()
	expires := time.Now().Add(sessionLifetime)
	m.mu.Lock()
	for key, s := range m.sessions {
		if time.Now().After(s.expires) {
			delete(m.sessions, key)
		}
	}
	m.sessions[sha256.Sum256([]byte(token))] = adminSession{user: user, expires: expires}
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureAuthRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: expires})
}
func secureAuthRequest(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return r.TLS != nil || (ip != nil && ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}
func sameAuthOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func authJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return false
	}
	if !sameAuthOrigin(r) {
		http.Error(w, "请求来源不匹配", 403)
		return false
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		http.Error(w, "请使用 JSON 格式提交", 415)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "请求格式错误", 400)
		return false
	}
	return true
}
func (a *app) authStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.auth == nil {
		http.Error(w, "认证服务未初始化", 503)
		return
	}
	user, logged := a.auth.sessionUser(r)
	writeJSON(w, map[string]any{"initialized": a.auth.initialized(), "authenticated": logged, "username": user, "build": buildinfo.Current()})
}
func (a *app) authSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.auth == nil {
		http.Error(w, "认证服务未初始化", 503)
		return
	}
	if a.auth.initialized() {
		http.Error(w, errAdminExists.Error(), 409)
		return
	}
	var in struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirmPassword"`
	}
	if !authJSON(w, r, &in) {
		return
	}
	if in.Password != in.ConfirmPassword {
		http.Error(w, "两次输入的密码不一致", 400)
		return
	}
	user := strings.TrimSpace(in.Username)
	if err := validateAdminInput(user, in.Password); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := a.auth.create(user, in.Password, true); err != nil {
		code := 500
		message := "管理员配置保存失败，请检查数据目录的写入权限"
		if errors.Is(err, errAdminExists) {
			code = 409
			message = errAdminExists.Error()
		}
		http.Error(w, message, code)
		return
	}
	a.auth.issueSession(w, r, user)
	writeJSON(w, map[string]any{"initialized": true, "username": user})
}
func (a *app) authLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.auth == nil || !a.auth.initialized() {
		http.Error(w, "请先完成首次安装", 409)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !authJSON(w, r, &in) {
		return
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	a.auth.mu.Lock()
	limit := a.auth.limits[peer]
	a.auth.mu.Unlock()
	if limit.failures >= 5 && now.Before(limit.until) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "尝试次数过多，请一分钟后再试", 429)
		return
	}
	if !a.auth.verify(strings.TrimSpace(in.Username), in.Password) {
		a.auth.mu.Lock()
		limit = a.auth.limits[peer]
		if !now.Before(limit.until) {
			limit = loginLimit{until: now.Add(time.Minute)}
		}
		limit.failures++
		a.auth.limits[peer] = limit
		for key, v := range a.auth.limits {
			if now.After(v.until) {
				delete(a.auth.limits, key)
			}
		}
		a.auth.mu.Unlock()
		http.Error(w, "账号或密码错误", 401)
		return
	}
	a.auth.mu.Lock()
	delete(a.auth.limits, peer)
	a.auth.mu.Unlock()
	a.auth.issueSession(w, r, strings.TrimSpace(in.Username))
	writeJSON(w, map[string]any{"username": strings.TrimSpace(in.Username)})
}
func (a *app) authLogout(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !authJSON(w, r, &in) {
		return
	}
	if a.auth != nil {
		if c, e := r.Cookie(adminCookie); e == nil {
			a.auth.mu.Lock()
			delete(a.auth.sessions, sha256.Sum256([]byte(c.Value)))
			a.auth.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Path: "/", Value: "", MaxAge: -1, HttpOnly: true, Secure: secureAuthRequest(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(204)
}

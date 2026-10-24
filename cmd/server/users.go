package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

var regularUsernamePattern = regexp.MustCompile(`^[\p{Han}A-Za-z0-9][\p{Han}A-Za-z0-9_.-]{2,31}$`)

const regularUsernameHint = "账号需为 3–32 个字符，可用中文、英文字母、数字、下划线、点或短横线，首位为中文、字母或数字"

func validRegularUsername(name string) bool {
	return utf8.ValidString(name) && regularUsernamePattern.MatchString(name)
}

type deviceGrant struct {
	DeviceID string `json:"deviceId"`
	SSH      bool   `json:"ssh"`
	Files    bool   `json:"files"`
}
type regularUser struct {
	ID           string        `json:"id"`
	Username     string        `json:"username"`
	PasswordHash string        `json:"passwordHash"`
	Enabled      bool          `json:"enabled"`
	Grants       []deviceGrant `json:"grants"`
	ExpiresAt    *time.Time    `json:"expiresAt,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}
type userView struct {
	ID        string        `json:"id"`
	Username  string        `json:"username"`
	Enabled   bool          `json:"enabled"`
	Grants    []deviceGrant `json:"grants"`
	ExpiresAt *time.Time    `json:"expiresAt,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

func (u regularUser) active(now time.Time) bool {
	return u.Enabled && (u.ExpiresAt == nil || now.Before(*u.ExpiresAt))
}
func (u regularUser) view() userView {
	v := userView{u.ID, u.Username, u.Enabled, append([]deviceGrant{}, u.Grants...), u.ExpiresAt, u.CreatedAt, u.UpdatedAt}
	if v.ExpiresAt != nil {
		value := *v.ExpiresAt
		v.ExpiresAt = &value
	}
	return v
}
func validateRegularUsers(record adminRecord) error {
	if len(record.Users) > 200 {
		return errors.New("普通用户数量超过上限")
	}
	seen := map[string]bool{strings.ToLower(record.Username): true}
	ids := map[string]bool{}
	for _, u := range record.Users {
		cost, err := bcrypt.Cost([]byte(u.PasswordHash))
		key := strings.ToLower(u.Username)
		if u.ID == "" || ids[u.ID] || seen[key] || !validRegularUsername(u.Username) || err != nil || cost < 10 || cost > 14 || len(u.Grants) > 1000 {
			return errors.New("普通用户配置无效，请恢复账号数据备份")
		}
		seen[key] = true
		ids[u.ID] = true
	}
	return nil
}
func (m *adminManager) validSessionLocked(s adminSession) bool {
	if m.record == nil || !time.Now().Before(s.expires) {
		return false
	}
	if s.user == m.record.Username {
		return true
	}
	for _, u := range m.record.Users {
		if u.Username == s.user {
			return u.active(time.Now())
		}
	}
	return false
}

type accountIdentity struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (m *adminManager) identity(r *http.Request) (accountIdentity, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range []string{adminCookie, httpAdminCookie} {
		cookie, err := r.Cookie(name)
		if err != nil {
			continue
		}
		key := sha256.Sum256([]byte(cookie.Value))
		s, ok := m.sessions[key]
		if !ok || !m.validSessionLocked(s) {
			delete(m.sessions, key)
			continue
		}
		role := "user"
		if s.user == m.record.Username {
			role = "admin"
		}
		return accountIdentity{s.user, role}, true
	}
	return accountIdentity{}, false
}
func (m *adminManager) ownerAllows(owner [32]byte, device, capability string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[owner]
	if !ok || !m.validSessionLocked(session) {
		return false
	}
	if session.user == m.record.Username {
		return true
	}
	for _, u := range m.record.Users {
		if u.Username == session.user {
			for _, g := range u.Grants {
				if g.DeviceID == device {
					return capability == "view" || capability == "remote" && (g.SSH || g.Files) || capability == "ssh" && g.SSH || capability == "files" && g.Files
				}
			}
		}
	}
	return false
}

// All account mutations replace one complete private file. A failed save must
// leave both the previous grants and live sessions intact.
func (m *adminManager) saveUsersLocked(users []regularUser) error {
	record := *m.record
	record.Users = users
	if err := validateRegularUsers(record); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(m.path), ".users-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), m.path)
	}
	if err != nil {
		return err
	}
	m.record = &record
	return nil
}

type userInput struct {
	ID        string        `json:"id"`
	Username  string        `json:"username"`
	Password  string        `json:"password"`
	Enabled   bool          `json:"enabled"`
	Grants    []deviceGrant `json:"grants"`
	ExpiresAt *time.Time    `json:"expiresAt"`
}

func (m *adminManager) putUser(in userInput) (userView, [][32]byte, error) {
	in.Username = strings.TrimSpace(in.Username)
	if !validRegularUsername(in.Username) {
		return userView{}, nil, errors.New(regularUsernameHint)
	}
	if len(in.Grants) > 1000 {
		return userView{}, nil, errors.New("每个用户最多授权 1000 台设备")
	}
	if in.ExpiresAt != nil && (in.ExpiresAt.Year() < 2000 || in.ExpiresAt.Year() > 2100) {
		return userView{}, nil, errors.New("授权有效期格式无效")
	}
	var hash string
	if in.Password != "" {
		if err := validateAccountPassword(in.Password); err != nil {
			return userView{}, nil, err
		}
		encoded, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return userView{}, nil, err
		}
		hash = string(encoded)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record == nil {
		return userView{}, nil, errors.New("管理员未初始化")
	}
	if strings.EqualFold(in.Username, m.record.Username) {
		return userView{}, nil, errors.New("普通用户不能与管理员同名")
	}
	users := append([]regularUser(nil), m.record.Users...)
	index := -1
	for i, u := range users {
		if u.ID == in.ID && in.ID != "" {
			index = i
		}
		if strings.EqualFold(u.Username, in.Username) && u.ID != in.ID {
			return userView{}, nil, errors.New("账号已存在")
		}
	}
	if in.ID != "" && index < 0 {
		return userView{}, nil, errors.New("用户不存在")
	}
	if index < 0 && len(users) >= 200 {
		return userView{}, nil, errors.New("最多创建 200 个普通用户")
	}
	u := regularUser{ID: in.ID, Username: in.Username, Enabled: in.Enabled, Grants: append([]deviceGrant{}, in.Grants...), ExpiresAt: in.ExpiresAt, UpdatedAt: time.Now().UTC()}
	oldName := ""
	if index >= 0 {
		u.ID = users[index].ID
		u.CreatedAt = users[index].CreatedAt
		u.PasswordHash = users[index].PasswordHash
		oldName = users[index].Username
	} else {
		u.ID = makeID()
		u.CreatedAt = u.UpdatedAt
	}
	if hash != "" {
		u.PasswordHash = hash
	}
	if u.PasswordHash == "" {
		return userView{}, nil, errors.New("新用户请设置至少 8 个字符的密码")
	}
	if index < 0 {
		users = append(users, u)
	} else {
		users[index] = u
	}
	if err := m.saveUsersLocked(users); err != nil {
		return userView{}, nil, errors.New("用户保存失败，请检查数据目录")
	}
	revoked := [][32]byte{}
	for key, s := range m.sessions {
		if s.user == oldName || s.user == u.Username {
			revoked = append(revoked, key)
			delete(m.sessions, key)
		}
	}
	return u.view(), revoked, nil
}

func (a *app) closeRevokedOwners(owners [][32]byte) {
	a.remoteMu.Lock()
	manager := a.remote
	a.remoteMu.Unlock()
	for _, owner := range owners {
		a.revokeOwnerMappingAccess(owner)
		if manager != nil {
			manager.closeOwner(owner)
		}
	}
}

func (a *app) usersHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !sameAuthOrigin(r) {
		http.Error(w, "请求来源不匹配", 403)
		return
	}
	if r.Method == "GET" {
		a.auth.mu.Lock()
		users := []userView{}
		for _, u := range a.auth.record.Users {
			users = append(users, u.view())
		}
		a.auth.mu.Unlock()
		writeJSON(w, map[string]any{"users": users, "limit": 200})
		return
	}
	if r.Method == "DELETE" {
		id := r.URL.Query().Get("id")
		a.auth.mu.Lock()
		users := []regularUser{}
		name := ""
		for _, u := range a.auth.record.Users {
			if u.ID == id {
				name = u.Username
			} else {
				users = append(users, u)
			}
		}
		if name == "" {
			a.auth.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if err := a.auth.saveUsersLocked(users); err != nil {
			a.auth.mu.Unlock()
			http.Error(w, "用户删除失败", 500)
			return
		}
		owners := [][32]byte{}
		for key, s := range a.auth.sessions {
			if s.user == name {
				owners = append(owners, key)
				delete(a.auth.sessions, key)
			}
		}
		a.auth.mu.Unlock()
		a.closeRevokedOwners(owners)
		w.WriteHeader(204)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input userInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "用户设置无效", 400)
		return
	}
	known := map[string]bool{}
	for _, d := range a.store.Snapshot().Devices {
		known[d.ID] = true
	}
	seen := map[string]bool{}
	for _, grant := range input.Grants {
		if !known[grant.DeviceID] || seen[grant.DeviceID] {
			http.Error(w, "授权设备不存在或重复，请刷新后重试", 400)
			return
		}
		seen[grant.DeviceID] = true
	}
	result, owners, err := a.auth.putUser(input)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.closeRevokedOwners(owners)
	writeJSON(w, result)
}

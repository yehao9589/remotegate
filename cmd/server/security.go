package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func normalizeAdminEntry(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "/" {
		return value, nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = strings.TrimSuffix(value, "/")
	if !entryPattern.MatchString(value) {
		return "", errors.New("后台入口请填写 /my-panel 这样的路径，名称为 3–64 位字母、数字、下划线或短横线")
	}
	switch strings.ToLower(value) {
	case "/api", "/install", "/downloads", "/healthz":
		return "", errors.New("该路径由系统使用，请换一个后台入口")
	}
	return value, nil
}

func (m *adminManager) entryPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record == nil || m.record.EntryPath == "" {
		return "/"
	}
	return m.record.EntryPath
}

func (m *adminManager) securityInfo() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.record.EntryPath
	if entry == "" {
		entry = "/"
	}
	return map[string]any{"username": m.record.Username, "entryPath": entry, "updatedAt": m.record.UpdatedAt}
}

// Write the full account atomically; failed writes leave credentials and sessions unchanged.
func (m *adminManager) updateSecurity(current, entry, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record == nil || bcrypt.CompareHashAndPassword([]byte(m.record.PasswordHash), []byte(current)) != nil {
		return errCurrentPassword
	}
	record := *m.record
	if entry != "" {
		record.EntryPath = entry
	}
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		record.PasswordHash = string(hash)
	}
	record.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
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
	if err = os.Rename(temp.Name(), m.path); err != nil {
		return err
	}
	m.record = &record
	m.sessions = make(map[[32]byte]adminSession)
	return nil
}

func (a *app) securitySettings(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Error(w, "认证服务未初始化", 503)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, a.auth.securityInfo())
		return
	}
	var in struct {
		Action          string `json:"action"`
		CurrentPassword string `json:"currentPassword"`
		EntryPath       string `json:"entryPath"`
		NewPassword     string `json:"newPassword"`
		ConfirmPassword string `json:"confirmPassword"`
	}
	if !authJSON(w, r, &in) {
		return
	}
	entry, password := "", ""
	if in.Action == "entry" {
		var err error
		entry, err = normalizeAdminEntry(in.EntryPath)
		if err != nil || in.EntryPath == "" {
			if err == nil {
				err = errors.New("请填写后台入口")
			}
			http.Error(w, err.Error(), 400)
			return
		}
	} else if in.Action == "password" {
		if in.NewPassword != in.ConfirmPassword {
			http.Error(w, "两次输入的新密码不一致", 400)
			return
		}
		user := a.auth.securityInfo()["username"].(string)
		if err := validateAdminInput(user, in.NewPassword); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		password = in.NewPassword
	} else {
		http.Error(w, "请选择修改入口或密码", 400)
		return
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	a.auth.mu.Lock()
	limit := a.auth.limits[peer]
	a.auth.mu.Unlock()
	if limit.failures >= 5 && time.Now().Before(limit.until) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "尝试次数过多，请一分钟后再试", 429)
		return
	}
	if err := a.auth.updateSecurity(in.CurrentPassword, entry, password); err != nil {
		if errors.Is(err, errCurrentPassword) {
			a.auth.mu.Lock()
			limit = a.auth.limits[peer]
			if time.Now().After(limit.until) {
				limit = loginLimit{until: time.Now().Add(time.Minute)}
			}
			limit.failures++
			a.auth.limits[peer] = limit
			a.auth.mu.Unlock()
			http.Error(w, err.Error(), 400)
		} else {
			http.Error(w, "保存失败，请检查数据目录写入权限；原设置已保留", 500)
		}
		return
	}
	a.auth.mu.Lock()
	delete(a.auth.limits, peer)
	a.auth.mu.Unlock()
	clearAdminCookies(w)
	writeJSON(w, map[string]any{"entryPath": a.auth.entryPath(), "loginRequired": true})
}

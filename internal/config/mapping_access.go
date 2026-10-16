package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func LocalMappingHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".127.0.0.1.nip.io")
}

func ValidateAccessConsole(value string) error {
	u, err := url.Parse(value)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") || u.RawPath != "" || (u.Scheme != "https" && !(u.Scheme == "http" && LocalMappingHost(u.Hostname()))) {
		return errors.New("后台认证地址须为有效 HTTPS 地址；本机预览可使用 HTTP")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) == nil && host != "localhost" && !mappingHostPattern.MatchString(host) {
		return errors.New("后台认证地址的主机名无效")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("后台认证地址的端口范围为 1–65535")
		}
	}
	return nil
}

func normalizeMappingAccess(m *Mapping) error {
	if m.AccessMode == "" {
		m.AccessMode = "admin"
	}
	if m.AccessMode != "admin" && m.AccessMode != "public" {
		return errors.New("请选择后台登录验证或公开访问")
	}
	if m.AccessMode == "public" {
		m.AccessConsoleURL = ""
		return nil
	}
	if m.PublicScheme != "https" && !LocalMappingHost(m.Host) {
		return errors.New("开启登录保护请使用 HTTPS 公网入口")
	}
	if m.AccessConsoleURL != "" {
		if err := ValidateAccessConsole(m.AccessConsoleURL); err != nil {
			return err
		}
		u, _ := url.Parse(m.AccessConsoleURL)
		if strings.EqualFold(u.Hostname(), m.Host) {
			return errors.New("映射域名不能与后台认证域名相同")
		}
	}
	return nil
}

func mappingAccessRevision(m *Mapping, previous *Mapping) error {
	if previous != nil && previous.Host == m.Host && previous.Target == m.Target && previous.DeviceID == m.DeviceID && previous.Enabled == m.Enabled && previous.PublicScheme == m.PublicScheme && previous.PublicPort == m.PublicPort && previous.AccessMode == m.AccessMode && previous.AccessConsoleURL == m.AccessConsoleURL && previous.AccessRevision != "" {
		m.AccessRevision = previous.AccessRevision
		return nil
	}
	var err error
	m.AccessRevision, err = randomToken(18)
	return err
}

// Complete the legacy migration after an authenticated administrator visits the
// console. No browser-supplied administrator cookie is ever shared with mappings.
func (s *Store) BindMappingAccessConsole(origin string) error {
	if err := ValidateAccessConsole(origin); err != nil {
		return err
	}
	u, _ := url.Parse(origin)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]Mapping(nil), s.state.Mappings...)
	changed := false
	for i := range s.state.Mappings {
		m := &s.state.Mappings[i]
		if m.AccessMode != "public" && m.AccessConsoleURL == "" && !strings.EqualFold(m.Host, u.Hostname()) {
			m.AccessConsoleURL = origin
			changed = true
		}
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			s.state.Mappings = previous
			return err
		}
	}
	return nil
}

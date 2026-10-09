package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Device struct {
	PackageVersion string    `json:"packageVersion,omitempty"`
	Version        string    `json:"version,omitempty"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	TokenHash      string    `json:"tokenHash"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Mapping struct {
	Note         string `json:"note,omitempty"`
	ID           string `json:"id"`
	Host         string `json:"host"`
	DeviceID     string `json:"deviceId"`
	Target       string `json:"target"`
	PublicScheme string `json:"publicScheme,omitempty"`
	PublicPort   int    `json:"publicPort,omitempty"`
	Enabled      bool   `json:"enabled"`
}

var mappingHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type State struct {
	Domain   DomainSettings   `json:"domain"`
	Domains  []DomainSettings `json:"domains,omitempty"`
	Devices  []Device         `json:"devices"`
	Mappings []Mapping        `json:"mappings"`
}

type Store struct {
	pending map[string]Device
	mu      sync.RWMutex
	path    string
	state   State
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(data) > 0 && json.Unmarshal(data, &s.state) != nil {
		return nil, errors.New("invalid state file")
	}
	migrated := false
	for i := range s.state.Mappings {
		beforeScheme, beforePort := s.state.Mappings[i].PublicScheme, s.state.Mappings[i].PublicPort
		applyMappingPublicDefaults(&s.state.Mappings[i])
		migrated = migrated || beforeScheme != s.state.Mappings[i].PublicScheme || beforePort != s.state.Mappings[i].PublicPort
	}
	if migrated {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := State{Domain: s.state.Domain, Domains: append([]DomainSettings(nil), s.state.Domains...), Devices: make([]Device, len(s.state.Devices)), Mappings: make([]Mapping, len(s.state.Mappings))}
	out.Domain = cloneDomain(out.Domain)
	for i := range out.Domains {
		out.Domains[i] = cloneDomain(out.Domains[i])
	}
	copy(out.Devices, s.state.Devices)
	copy(out.Mappings, s.state.Mappings)
	sort.Slice(out.Devices, func(i, j int) bool { return out.Devices[i].Name < out.Devices[j].Name })
	sort.Slice(out.Mappings, func(i, j int) bool { return out.Mappings[i].Host < out.Mappings[j].Host })
	return out
}

func (s *Store) CreateDevice(name string) (Device, string, error) {
	return s.createDevice(name, false)
}

func (s *Store) CreatePendingDevice(name string) (Device, string, error) {
	return s.createDevice(name, true)
}

func (s *Store) createDevice(name string, pending bool) (Device, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Device{}, "", errors.New("device name is required")
	}
	token, err := randomToken(32)
	if err != nil {
		return Device{}, "", err
	}
	id, err := randomToken(9)
	if err != nil {
		return Device{}, "", err
	}
	d := Device{ID: id, Name: name, TokenHash: hash(token), CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending {
		if s.pending == nil {
			s.pending = make(map[string]Device)
		}
		for id, old := range s.pending {
			if time.Since(old.CreatedAt) > 24*time.Hour {
				delete(s.pending, id)
			}
		}
		s.pending[d.ID] = d
		return d, token, nil
	}
	s.state.Devices = append(s.state.Devices, d)
	return d, token, s.saveLocked()
}

func (s *Store) Authenticate(deviceID, token string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if d, ok := s.pending[deviceID]; ok && time.Since(d.CreatedAt) < 24*time.Hour && d.TokenHash == hash(token) {
		return d, true
	}
	for _, d := range s.state.Devices {
		if d.ID == deviceID && d.TokenHash == hash(token) {
			return d, true
		}
	}
	return Device{}, false
}

// Activate commits enrollment only after an authenticated WebSocket upgrade.
func (s *Store) Activate(id, token, version string) error {
	return s.ActivateWithPackage(id, token, version, "")
}
func (s *Store) ActivateWithPackage(id, token, version, packageVersion string) error {
	if len(version) > 128 || len(packageVersion) > 64 {
		return errors.New("invalid device version")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.state.Devices {
		if d.ID == id && d.TokenHash == hash(token) {
			if d.Version == version && d.PackageVersion == packageVersion {
				return nil
			}
			s.state.Devices[i].Version = version
			s.state.Devices[i].PackageVersion = packageVersion
			if err := s.saveLocked(); err != nil {
				s.state.Devices[i] = d
				return err
			}
			return nil
		}
	}
	d, ok := s.pending[id]
	if !ok || time.Since(d.CreatedAt) >= 24*time.Hour || d.TokenHash != hash(token) {
		return errors.New("enrollment expired")
	}
	d.Version = version
	d.PackageVersion = packageVersion
	s.state.Devices = append(s.state.Devices, d)
	if err := s.saveLocked(); err != nil {
		s.state.Devices = s.state.Devices[:len(s.state.Devices)-1]
		return err
	}
	delete(s.pending, id)
	return nil
}

func (s *Store) PutMapping(m Mapping) (Mapping, error) {
	m.Host = normalizeHost(m.Host)
	m.Target = strings.TrimRight(strings.TrimSpace(m.Target), "/")
	if m.Host == "" || len(m.Host) > 253 || !mappingHostPattern.MatchString(m.Host) || net.ParseIP(m.Host) != nil {
		return Mapping{}, errors.New("请填写有效的访问域名，不含协议、端口、路径或星号")
	}
	target, err := url.Parse(m.Target)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return Mapping{}, errors.New("内网地址必须是有效的 http(s) 地址，不能包含账号、查询参数或片段")
	}
	if port := target.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return Mapping{}, errors.New("内网端口范围为 1–65535")
		}
	}
	if m.DeviceID == "" {
		return Mapping{}, errors.New("请选择接入设备")
	}
	m.PublicScheme = strings.ToLower(strings.TrimSpace(m.PublicScheme))
	applyMappingPublicDefaults(&m)
	if m.PublicScheme != "http" && m.PublicScheme != "https" {
		return Mapping{}, errors.New("公网访问协议仅支持 HTTP 或 HTTPS")
	}
	if m.PublicPort < 1 || m.PublicPort > 65535 {
		return Mapping{}, errors.New("公网端口范围为 1–65535")
	}
	if m.ID == "" {
		m.ID, _ = randomToken(9)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	foundDevice := false
	for _, d := range s.state.Devices {
		if d.ID == m.DeviceID {
			foundDevice = true
		}
	}
	if !foundDevice {
		return Mapping{}, errors.New("device does not exist")
	}
	for _, existing := range s.state.Mappings {
		if existing.Host == m.Host && existing.ID != m.ID {
			return Mapping{}, errors.New("host already exists")
		}
	}
	for i, existing := range s.state.Mappings {
		if existing.ID == m.ID {
			s.state.Mappings[i] = m
			return m, s.saveLocked()
		}
	}
	s.state.Mappings = append(s.state.Mappings, m)
	return m, s.saveLocked()
}

func applyMappingPublicDefaults(m *Mapping) {
	if m.PublicScheme == "" {
		if m.Host == "localhost" || strings.HasSuffix(m.Host, ".localhost") || strings.HasSuffix(m.Host, ".127.0.0.1.nip.io") {
			m.PublicScheme = "http"
		} else {
			m.PublicScheme = "https"
		}
	}
	if m.PublicPort == 0 {
		if m.PublicScheme == "https" {
			m.PublicPort = 443
		} else {
			m.PublicPort = 80
			if m.Host == "localhost" || strings.HasSuffix(m.Host, ".localhost") || strings.HasSuffix(m.Host, ".127.0.0.1.nip.io") {
				if _, port, err := net.SplitHostPort(strings.TrimSpace(os.Getenv("LISTEN_ADDR"))); err == nil {
					if value, err := strconv.Atoi(port); err == nil && value >= 1 && value <= 65535 {
						m.PublicPort = value
					}
				}
			}
		}
	}
}

func (s *Store) ChangeDevice(id, name string, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.state.Devices {
		if d.ID != id {
			continue
		}
		if remove {
			for _, m := range s.state.Mappings {
				if m.DeviceID == id {
					return errors.New("请先删除此设备的域名映射")
				}
			}
			s.state.Devices = append(s.state.Devices[:i], s.state.Devices[i+1:]...)
		} else {
			name = strings.TrimSpace(name)
			if name == "" {
				return errors.New("设备名称不能为空")
			}
			s.state.Devices[i].Name = name
		}
		return s.saveLocked()
	}
	return os.ErrNotExist
}

func (s *Store) DeleteMapping(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.state.Mappings {
		if m.ID == id {
			s.state.Mappings = append(s.state.Mappings[:i], s.state.Mappings[i+1:]...)
			return s.saveLocked()
		}
	}
	return os.ErrNotExist
}

func (s *Store) MappingForHost(host string) (Mapping, bool) {
	host = normalizeHost(host)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.state.Mappings {
		if m.Enabled && m.Host == host {
			return m, true
		}
	}
	return Mapping{}, false
}

func normalizeHost(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSuffix(v, ".")
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hash(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }

package config

import (
	"errors"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

type DomainSettings struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	BaseDomain        string             `json:"baseDomain"`
	ServerIP          string             `json:"serverIP"`
	DNSProvider       string             `json:"dnsProvider"`
	RootHTTPSProvider string             `json:"rootHTTPSProvider,omitempty"`
	RootHTTPSPort     int                `json:"rootHTTPSPort,omitempty"`
	PublicHTTPS       *PublicHTTPSCheck  `json:"publicHTTPS,omitempty"`
	HTTPSChecks       []PublicHTTPSCheck `json:"httpsChecks,omitempty"`
}

// This is a server-observed result for one concrete public endpoint, not an
// installed certificate or evidence that all subdomains have HTTPS enabled.
type PublicHTTPSCheck struct {
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	CheckedAt time.Time `json:"checkedAt"`
	Valid     bool      `json:"valid"`
	Error     string    `json:"error,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	Names     []string  `json:"names,omitempty"`
	Addresses []string  `json:"addresses,omitempty"`
	NotBefore time.Time `json:"notBefore,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (s *Store) DomainSettings() DomainSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.state.Domains) > 0 {
		return cloneDomain(s.state.Domains[0])
	}
	return cloneDomain(s.state.Domain)
}
func normalizeDomain(d DomainSettings) (DomainSettings, error) {
	d.PublicHTTPS = nil // Observations cannot be supplied through the settings API.
	d.HTTPSChecks = nil
	if d.RootHTTPSProvider == "" {
		d.RootHTTPSProvider = "external"
	}
	if d.RootHTTPSProvider != "" && d.RootHTTPSProvider != "remotegate" && d.RootHTTPSProvider != "external" {
		return d, errors.New("请选择 RemoteGate 或宝塔 / 反向代理管理主域名 HTTPS")
	}
	if d.RootHTTPSPort < 0 || d.RootHTTPSPort > 65535 {
		return d, errors.New("公网 HTTPS 端口范围为 1–65535")
	}
	d.BaseDomain = strings.ToLower(strings.TrimSpace(d.BaseDomain))
	d.ServerIP = strings.TrimSpace(d.ServerIP)
	if d.BaseDomain == "" || len(d.BaseDomain) > 253 || !domainPattern.MatchString(d.BaseDomain) || net.ParseIP(d.BaseDomain) != nil {
		return d, errors.New("请填写主域名，例如 example.com，不含协议、端口或星号")
	}
	if d.ServerIP != "" && net.ParseIP(d.ServerIP) == nil {
		return d, errors.New("请填写有效的服务器公网 IP")
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		d.Name = d.BaseDomain
	}
	d.DNSProvider = strings.ToLower(strings.TrimSpace(d.DNSProvider))
	if d.DNSProvider == "" {
		d.DNSProvider = "manual"
	}
	if d.DNSProvider != "manual" && d.DNSProvider != "alidns" && d.DNSProvider != "dnspod" && d.DNSProvider != "cloudflare" {
		return d, errors.New("不支持的 DNS 平台")
	}
	return d, nil
}
func (s *Store) Domains() []DomainSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]DomainSettings{}, s.state.Domains...)
	for i := range out {
		out[i] = cloneDomain(out[i])
	}
	if len(out) == 0 && s.state.Domain.BaseDomain != "" {
		d := cloneDomain(s.state.Domain)
		if d.ID == "" {
			d.ID = "legacy"
		}
		if d.Name == "" {
			d.Name = d.BaseDomain
		}
		if d.DNSProvider == "" {
			d.DNSProvider = "manual"
		}
		out = []DomainSettings{d}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (s *Store) PutDomain(d DomainSettings) (DomainSettings, error) {
	var err error
	d, err = normalizeDomain(d)
	if err != nil {
		return cloneDomain(d), err
	}
	if d.ID == "" {
		d.ID, _ = randomToken(9)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Domains) == 0 && s.state.Domain.BaseDomain != "" {
		old := s.state.Domain
		if old.ID == "" {
			old.ID = "legacy"
		}
		if old.Name == "" {
			old.Name = old.BaseDomain
		}
		if old.DNSProvider == "" {
			old.DNSProvider = "manual"
		}
		s.state.Domains = []DomainSettings{old}
		s.state.Domain = DomainSettings{}
	}
	for _, v := range s.state.Domains {
		if v.BaseDomain == d.BaseDomain && v.ID != d.ID {
			return d, errors.New("该主域名已经存在")
		}
	}
	for i, v := range s.state.Domains {
		if v.ID == d.ID {
			old := v
			if old.BaseDomain == d.BaseDomain && old.ServerIP == d.ServerIP && effectiveHTTPSPort(old) == effectiveHTTPSPort(d) {
				d.PublicHTTPS = old.PublicHTTPS
				d.HTTPSChecks = old.HTTPSChecks
			}
			s.state.Domains[i] = d
			if err = s.saveLocked(); err != nil {
				s.state.Domains[i] = old
			}
			return cloneDomain(d), err
		}
	}
	s.state.Domains = append(s.state.Domains, d)
	if err = s.saveLocked(); err != nil {
		s.state.Domains = s.state.Domains[:len(s.state.Domains)-1]
	}
	return d, err
}
func (s *Store) DeleteDomain(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.state.Domains {
		if d.ID == id {
			for _, m := range s.state.Mappings {
				if m.Host == d.BaseDomain || strings.HasSuffix(m.Host, "."+d.BaseDomain) {
					return errors.New("请先删除此域名下的映射")
				}
			}
			s.state.Domains = append(s.state.Domains[:i], s.state.Domains[i+1:]...)
			return s.saveLocked()
		}
	}
	return errors.New("域名不存在")
}
func (s *Store) SetDomainSettings(d DomainSettings) error { _, err := s.PutDomain(d); return err }

func effectiveHTTPSPort(d DomainSettings) int {
	if d.RootHTTPSPort == 0 {
		return 443
	}
	return d.RootHTTPSPort
}

func cloneDomain(d DomainSettings) DomainSettings {
	if d.PublicHTTPS != nil {
		check := *d.PublicHTTPS
		check.Names = append([]string(nil), check.Names...)
		check.Addresses = append([]string(nil), check.Addresses...)
		d.PublicHTTPS = &check
	}
	d.HTTPSChecks = append([]PublicHTTPSCheck(nil), d.HTTPSChecks...)
	for i := range d.HTTPSChecks {
		d.HTTPSChecks[i].Names = append([]string(nil), d.HTTPSChecks[i].Names...)
		d.HTTPSChecks[i].Addresses = append([]string(nil), d.HTTPSChecks[i].Addresses...)
	}
	return d
}

// Store concrete endpoint observations separately; a wildcard SAN is never
// evidence that another endpoint returns that certificate.
func (s *Store) SetDomainHTTPSChecks(id string, checks []PublicHTTPSCheck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var d *DomainSettings
	for i := range s.state.Domains {
		if s.state.Domains[i].ID == id {
			d = &s.state.Domains[i]
			break
		}
	}
	if d == nil && id == "legacy" && s.state.Domain.BaseDomain != "" {
		d = &s.state.Domain
	}
	if d == nil {
		return errors.New("域名不存在，请刷新列表")
	}
	old := cloneDomain(*d)
	next := cloneDomain(*d)
	for _, check := range checks {
		if check.Port < 1 || check.Port > 65535 || len(check.Host) > 253 || !domainPattern.MatchString(check.Host) || (check.Host != d.BaseDomain && !strings.HasSuffix(check.Host, "."+d.BaseDomain)) {
			return errors.New("检测结果与当前域名不匹配，请重新检测")
		}
		check.Names = append([]string(nil), check.Names...)
		check.Addresses = append([]string(nil), check.Addresses...)
		kept := next.HTTPSChecks[:0]
		for _, existing := range next.HTTPSChecks {
			if existing.Host != check.Host || existing.Port != check.Port {
				kept = append(kept, existing)
			}
		}
		next.HTTPSChecks = append(kept, check)
		if len(next.HTTPSChecks) > 40 {
			next.HTTPSChecks = next.HTTPSChecks[len(next.HTTPSChecks)-40:]
		}
		if check.Host == d.BaseDomain && check.Port == effectiveHTTPSPort(*d) {
			root := check
			next.PublicHTTPS = &root
		}
	}
	*d = next
	if err := s.saveLocked(); err != nil {
		*d = old
		return err
	}
	return nil
}

func (s *Store) SetDomainHTTPSCheck(id string, check PublicHTTPSCheck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.state.Domains {
		if d.ID != id {
			continue
		}
		if check.Host != d.BaseDomain || check.Port != effectiveHTTPSPort(d) {
			return errors.New("检测结果与当前主域名 HTTPS 入口不匹配，请重新检测")
		}
		old := d.PublicHTTPS
		check.Names = append([]string(nil), check.Names...)
		s.state.Domains[i].PublicHTTPS = &check
		if err := s.saveLocked(); err != nil {
			s.state.Domains[i].PublicHTTPS = old
			return err
		}
		return nil
	}
	// Legacy single-domain state is still readable without requiring a settings edit.
	if id == "legacy" && s.state.Domain.BaseDomain == check.Host && effectiveHTTPSPort(s.state.Domain) == check.Port {
		old := s.state.Domain.PublicHTTPS
		check.Names = append([]string(nil), check.Names...)
		s.state.Domain.PublicHTTPS = &check
		if err := s.saveLocked(); err != nil {
			s.state.Domain.PublicHTTPS = old
			return err
		}
		return nil
	}
	return errors.New("域名不存在，请刷新列表")
}

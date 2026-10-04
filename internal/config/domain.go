package config

import (
	"errors"
	"net"
	"regexp"
	"sort"
	"strings"
)

type DomainSettings struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseDomain  string `json:"baseDomain"`
	ServerIP    string `json:"serverIP"`
	DNSProvider string `json:"dnsProvider"`
}

var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (s *Store) DomainSettings() DomainSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.state.Domains) > 0 {
		return s.state.Domains[0]
	}
	return s.state.Domain
}
func normalizeDomain(d DomainSettings) (DomainSettings, error) {
	d.BaseDomain = strings.ToLower(strings.TrimSpace(d.BaseDomain))
	d.ServerIP = strings.TrimSpace(d.ServerIP)
	if d.BaseDomain == "" || len(d.BaseDomain) > 253 || !domainPattern.MatchString(d.BaseDomain) || net.ParseIP(d.BaseDomain) != nil {
		return d, errors.New("请填写主域名，例如 fanke.xyz，不含协议、端口或星号")
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
	if len(out) == 0 && s.state.Domain.BaseDomain != "" {
		d := s.state.Domain
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
		return d, err
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
			s.state.Domains[i] = d
			if err = s.saveLocked(); err != nil {
				s.state.Domains[i] = old
			}
			return d, err
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

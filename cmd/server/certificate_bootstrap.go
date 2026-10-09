package main

import (
	"errors"
	"log"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
)

// Optional first-install provisioning gives an independent deployment a trusted
// HTTPS installation page without an HTTP login or an external reverse proxy.
// Once an administrator exists, environment values never replace UI settings.
func bootstrapCertificateState(getenv func(string) string) (certificateState, bool, error) {
	value := func(key string) string { return strings.TrimSpace(getenv(key)) }
	domain := strings.ToLower(value("BOOTSTRAP_DOMAIN"))
	if domain == "" {
		return certificateState{}, false, nil
	}
	fail := func(message string) (certificateState, bool, error) {
		return certificateState{}, true, errors.New(message)
	}
	if len(domain) > 253 || !certDomainPattern.MatchString(domain) || net.ParseIP(domain) != nil {
		return fail("BOOTSTRAP_DOMAIN 请填写后台域名，不含协议、端口或星号")
	}
	email := value("BOOTSTRAP_EMAIL")
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return fail("BOOTSTRAP_EMAIL 请填写有效邮箱")
	}
	provider := strings.ToLower(value("BOOTSTRAP_DNS_PROVIDER"))
	if provider != "alidns" && provider != "dnspod" && provider != "cloudflare" {
		return fail("BOOTSTRAP_DNS_PROVIDER 请选择 alidns、dnspod 或 cloudflare")
	}
	access, secret := value("BOOTSTRAP_DNS_ACCESS_KEY"), value("BOOTSTRAP_DNS_SECRET_KEY")
	if access == "" || (provider == "alidns" && secret == "") {
		return fail("请填写首次证书所需的 DNS 授权凭据")
	}
	terms, err := strconv.ParseBool(value("BOOTSTRAP_ACME_TERMS_ACCEPTED"))
	if err != nil || !terms {
		return fail("阅读 Let's Encrypt 服务条款后，将 BOOTSTRAP_ACME_TERMS_ACCEPTED 设置为 true")
	}
	return certificateState{Domain: domain, Names: []string{domain}, Provider: provider, Email: email, AccessKey: access, SecretKey: secret, CA: "letsencrypt", KeyType: "2048", AutoRenew: true, TermsAccepted: true, PendingACME: true}, true, nil
}

func (a *app) bootstrapCertificate(getenv func(string) string) error {
	if a.auth == nil || a.auth.initialized() {
		return nil
	}
	s, enabled, err := bootstrapCertificateState(getenv)
	if err != nil || !enabled {
		return err
	}
	status := a.httpsStatus()
	if status["running"] != true || status["mode"] != "direct" {
		return errors.New("首次证书需要独立 HTTPS 入口已启动；请检查 HTTPS_LISTEN_ADDR 与端口占用")
	}
	m, err := a.certificateFor(s.Domain)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.pair != nil {
		m.mu.Unlock()
		return nil
	} // Never overwrite an installed certificate.
	if m.state.Domain != "" && m.state.Domain != s.Domain {
		m.mu.Unlock()
		return errors.New("首次证书与已有域名配置不一致")
	}
	s.LastAttempt = m.state.LastAttempt
	s.History = append([]certificateEvent(nil), m.state.History...)
	addCertificateEvent(&s, "首次 HTTPS 配置", "success", "独立部署的后台证书配置已保存")
	err = m.saveLocked(s)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	found := false
	for _, domain := range a.store.Domains() {
		if domain.BaseDomain == s.Domain {
			found = true
			break
		}
	}
	if !found {
		if _, err = a.store.PutDomain(config.DomainSettings{BaseDomain: s.Domain, DNSProvider: s.Provider, RootHTTPSProvider: "remotegate", RootHTTPSPort: 443}); err != nil {
			return err
		}
	}
	if err = m.start(s.Domain); err != nil {
		return err
	}
	log.Printf("首次 HTTPS：正在为 %s 申请后台证书；DNS 验证可能需要几分钟", s.Domain)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.mu.Lock()
			running, result := m.running, m.state.LastResult
			m.mu.Unlock()
			if !running {
				log.Printf("首次 HTTPS：%s；请访问后台域名的 /install，失败时检查配置后重启重试", result)
				return
			}
		}
	}()
	return nil
}

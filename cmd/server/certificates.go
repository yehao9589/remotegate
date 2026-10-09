package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type certificateState struct {
	History         []certificateEvent `json:"history,omitempty"`
	Names           []string           `json:"names,omitempty"`
	CA              string             `json:"ca,omitempty"`
	KeyType         string             `json:"keyType,omitempty"`
	EABKeyID        string             `json:"eabKeyId,omitempty"`
	EABHMAC         string             `json:"eabHmac,omitempty"`
	CertificatePath string             `json:"certificatePath,omitempty"`
	PrivateKeyPath  string             `json:"privateKeyPath,omitempty"`
	Domain          string             `json:"domain"`
	Provider        string             `json:"provider"`
	Email           string             `json:"email"`
	AccessKey       string             `json:"accessKey"`
	SecretKey       string             `json:"secretKey"`
	AutoRenew       bool               `json:"autoRenew"`
	RenewBeforeDays int                `json:"renewBeforeDays,omitempty"`
	RetryHours      int                `json:"retryHours,omitempty"`
	PendingACME     bool               `json:"pendingACME,omitempty"`
	TermsAccepted   bool               `json:"termsAccepted"`
	CertPEM         string             `json:"certPEM,omitempty"`
	KeyPEM          string             `json:"keyPEM,omitempty"`
	Source          string             `json:"source,omitempty"`
	UpdatedAt       time.Time          `json:"updatedAt,omitempty"`
	LastAttempt     time.Time          `json:"lastAttempt,omitempty"`
	LastResult      string             `json:"lastResult,omitempty"`
}

type certificateManager struct {
	mu         sync.Mutex
	path       string
	state      certificateState
	pair       *tls.Certificate
	running    bool
	tlsAddress string
	tlsError   string
	obtain     func(certificateState) (string, string, error)
}

func newCertificateManager(path string) (*certificateManager, error) {
	m := &certificateManager{path: path}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &m.state); err != nil {
			return nil, err
		}
	}
	if m.state.CertPEM != "" {
		pair, err := tls.X509KeyPair([]byte(m.state.CertPEM), []byte(m.state.KeyPEM))
		if err != nil {
			return nil, err
		}
		m.pair = &pair
	}
	if n := len(m.state.History); n > 0 && m.state.History[n-1].Status == "running" {
		s := m.state
		s.LastResult = "上次申请因服务重启中断，请检查配置后重试；已有证书继续使用"
		addCertificateEvent(&s, "任务中断", "error", s.LastResult)
		if err := m.saveLocked(s); err != nil {
			return nil, err
		}
	}
	m.obtain = func(s certificateState) (string, string, error) { return obtainACME(filepath.Dir(path), s) }
	return m, nil
}
func (m *certificateManager) saveLocked(s certificateState) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, m.path); err != nil {
		return err
	}
	m.state = s
	return nil
}
func validateCertificate(certPEM, keyPEM, domain string) (*tls.Certificate, *x509.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, nil, errors.New("证书或私钥格式不正确，或二者不匹配")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, errors.New("无法解析证书")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, nil, errors.New("证书尚未生效或已经过期")
	}
	if leaf.IsCA {
		return nil, nil, errors.New("请上传服务器证书，不要上传 CA 根证书")
	}
	if domain == "" {
		return nil, nil, errors.New("请先保存主域名")
	}
	found := false
	for _, name := range leaf.DNSNames {
		if strings.EqualFold(name, domain) || strings.HasSuffix(strings.ToLower(name), "."+domain) {
			found = true
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("证书必须覆盖 %s 或它的子域名", domain)
	}
	if len(leaf.ExtKeyUsage) > 0 {
		server := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				server = true
			}
		}
		if !server {
			return nil, nil, errors.New("证书不支持服务器身份认证")
		}
	}
	pair.Leaf = leaf
	return &pair, leaf, nil
}
func (m *certificateManager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pair == nil {
		return nil, errors.New("no TLS certificate installed")
	}
	leaf := m.pair.Leaf
	if leaf == nil {
		var err error
		leaf, err = x509.ParseCertificate(m.pair.Certificate[0])
		if err != nil {
			return nil, err
		}
	}
	if time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, errors.New("TLS certificate not valid now")
	}
	if hello.ServerName != "" {
		if err := leaf.VerifyHostname(hello.ServerName); err != nil {
			return nil, err
		}
	}
	return m.pair, nil
}
func (m *certificateManager) status(domain string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	credentialsConfigured := s.AccessKey != "" && (s.Provider != "alidns" || s.SecretKey != "")
	out := map[string]any{"domain": s.Domain, "provider": s.Provider, "email": s.Email, "credentialsConfigured": credentialsConfigured, "autoRenew": s.AutoRenew, "termsAccepted": s.TermsAccepted, "running": m.running, "source": s.Source, "updatedAt": s.UpdatedAt, "lastAttempt": s.LastAttempt, "lastResult": s.LastResult, "httpsAddress": m.tlsAddress, "httpsError": m.tlsError, "domainMatches": s.Domain == domain, "installed": m.pair != nil}
	if m.pair != nil {
		leaf, err := x509.ParseCertificate(m.pair.Certificate[0])
		if err == nil {
			out["domainMatches"] = s.Domain == domain
			out["certificate"] = map[string]any{"names": leaf.DNSNames, "issuer": leaf.Issuer.CommonName, "expiresAt": leaf.NotAfter, "notBefore": leaf.NotBefore, "daysRemaining": int(time.Until(leaf.NotAfter).Hours() / 24), "valid": !time.Now().Before(leaf.NotBefore) && time.Now().Before(leaf.NotAfter)}
		}
	}
	out["names"] = requestedNames(s)
	out["ca"] = caName(s)
	out["keyType"] = s.KeyType
	out["eabConfigured"] = s.EABKeyID != "" && s.EABHMAC != ""
	out["certificatePath"] = s.CertificatePath
	out["privateKeyPath"] = s.PrivateKeyPath
	out["history"] = append([]certificateEvent{}, s.History...)
	out["configured"] = s.Domain == domain && credentialsConfigured && s.Email != "" && s.TermsAccepted
	out["renewBeforeDays"], out["retryHours"] = renewalPolicy(s)
	out["pendingACME"] = s.PendingACME
	if !s.LastAttempt.IsZero() {
		out["retryAfter"] = s.LastAttempt.Add(time.Minute)
	}
	if m.pair != nil && s.Source == "acme" && s.AutoRenew && !s.PendingACME {
		if leaf, err := x509.ParseCertificate(m.pair.Certificate[0]); err == nil {
			due := renewalTime(s, leaf)
			out["renewAfter"] = due
		}
	}
	return out
}
func (m *certificateManager) start(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("证书任务正在运行，请等待完成")
	}
	s := m.state
	if domain == "" || s.Domain != domain {
		return errors.New("主域名已变更，请重新保存证书申请配置")
	}
	if s.AccessKey == "" || (s.Provider == "alidns" && s.SecretKey == "") || s.Email == "" || !s.TermsAccepted {
		return errors.New("请填写 DNS 平台授权、邮箱并确认 ACME 服务条款")
	}
	if !s.LastAttempt.IsZero() && time.Since(s.LastAttempt) < time.Minute {
		return errors.New("请至少等待一分钟后再重试")
	}
	s.LastAttempt = time.Now().UTC()
	// Saving a draft does not apply it. Once an existing ACME certificate's
	// replacement is explicitly requested, failed attempts may retry normally.
	if s.Source == "acme" {
		s.PendingACME = false
	}
	s.LastResult = "正在申请：DNS 验证可能需要几分钟"
	operation := "证书申请"
	if m.pair != nil && s.Source == "acme" {
		operation = "证书续期"
	}
	addCertificateEvent(&s, operation+"开始", "running", "正在进行 ACME DNS 验证")
	if err := m.saveLocked(s); err != nil {
		return err
	}
	m.running = true
	go func() {
		cert, key, err := m.obtain(s)
		var pair *tls.Certificate
		if err == nil {
			pair, _, err = validateCertificate(cert, key, domain)
			if err == nil {
				err = verifyRequestedNames(pair, requestedNames(s))
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		defer func() { m.running = false }()
		next := m.state
		if err != nil {
			msg := err.Error()
			for _, secret := range []string{s.AccessKey, s.SecretKey, s.EABKeyID, s.EABHMAC} {
				if secret != "" {
					msg = strings.ReplaceAll(msg, secret, "[已隐藏]")
				}
			}
			if len(msg) > 1800 {
				msg = msg[:1800]
			}
			next.LastResult = "申请失败：" + msg
			addCertificateEvent(&next, operation, "error", next.LastResult)
		} else {
			next.CertPEM = cert
			next.KeyPEM = key
			next.Source = "acme"
			next.PendingACME = false
			next.UpdatedAt = time.Now().UTC()
			next.LastResult = "证书已签发并保存；内置 HTTPS 使用新证书，外部反向代理需另行部署"
			addCertificateEvent(&next, operation, "success", next.LastResult)
		}
		if e := m.saveLocked(next); e != nil {
			m.state.LastResult = "证书保存失败，请检查数据目录权限"
			return
		}
		if err == nil {
			m.pair = pair
		}
	}()
	return nil
}
func (m *certificateManager) renewDue(domain string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	if m.running || !s.AutoRenew || s.PendingACME || s.Source != "acme" || s.Domain != domain || m.pair == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(m.pair.Certificate[0])
	return err == nil && !now.Before(renewalTime(s, leaf))
}

// Zero values preserve the scheduling policy used by older installations.
func renewalPolicy(s certificateState) (days, hours int) {
	days, hours = s.RenewBeforeDays, s.RetryHours
	if days == 0 {
		days = 30
	}
	if hours == 0 {
		hours = 12
	}
	return
}

func renewalTime(s certificateState, leaf *x509.Certificate) time.Time {
	days, hours := renewalPolicy(s)
	due := leaf.NotAfter.Add(-time.Duration(days) * 24 * time.Hour)
	// Avoid constant reissuance for certificates shorter than the lead window.
	// Such certificates become eligible after two thirds of their lifetime.
	if adaptive := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3); due.Before(adaptive) && leaf.NotAfter.Sub(leaf.NotBefore) <= time.Duration(days)*24*time.Hour {
		due = adaptive
	}
	if !s.LastAttempt.IsZero() {
		if backoff := s.LastAttempt.Add(time.Duration(hours) * time.Hour); backoff.After(due) {
			due = backoff
		}
	}
	return due
}

func setRenewalPolicy(s *certificateState, days, hours *int) error {
	if days != nil && (*days < 1 || *days > 90) {
		return errors.New("续期提前天数应为 1–90 天")
	}
	if hours != nil && (*hours < 1 || *hours > 168) {
		return errors.New("失败重试间隔应为 1–168 小时")
	}
	if days != nil {
		s.RenewBeforeDays = *days
	}
	if hours != nil {
		s.RetryHours = *hours
	}
	return nil
}

func acmeConfigurationChanged(before, after certificateState) bool {
	keyType := func(s certificateState) string {
		if s.KeyType == "" {
			return "2048"
		}
		return s.KeyType
	}
	oldNames, newNames := slices.Clone(requestedNames(before)), slices.Clone(requestedNames(after))
	slices.Sort(oldNames)
	slices.Sort(newNames)
	return before.Domain != after.Domain || before.Provider != after.Provider || before.Email != after.Email ||
		caName(before) != caName(after) || keyType(before) != keyType(after) ||
		before.AccessKey != after.AccessKey || before.SecretKey != after.SecretKey ||
		before.EABKeyID != after.EABKeyID || before.EABHMAC != after.EABHMAC || !slices.Equal(oldNames, newNames)
}
func (a *app) certificates(w http.ResponseWriter, r *http.Request) {
	var domain string
	for _, d := range a.store.Domains() {
		if d.ID == r.URL.Query().Get("domainId") {
			domain = d.BaseDomain
			break
		}
	}
	if domain == "" && a.certs != nil {
		domain = a.store.DomainSettings().BaseDomain
	}
	if domain == "" && a.certs == nil {
		http.Error(w, "域名不存在，请刷新列表", http.StatusNotFound)
		return
	}
	m, managerErr := a.certificateFor(domain)
	if managerErr != nil {
		http.Error(w, managerErr.Error(), 500)
		return
	}
	if m == nil {
		http.Error(w, "证书管理未初始化", 503)
		return
	}
	if r.Method == "GET" {
		status := m.status(domain)
		https := a.httpsStatus()
		status["httpsAddress"] = https["address"]
		status["httpsError"] = https["error"]
		matches := []map[string]any{}
		for _, mapping := range a.store.Snapshot().Mappings {
			if mapping.Host == domain || strings.HasSuffix(mapping.Host, "."+domain) {
				_, err := m.getCertificate(&tls.ClientHelloInfo{ServerName: mapping.Host})
				matches = append(matches, map[string]any{"host": mapping.Host, "covered": err == nil})
			}
		}
		status["mappings"] = matches
		status["httpsSettings"] = https
		writeJSON(w, status)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	// Credentials may only be sent over HTTPS or a loopback development connection.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if r.TLS == nil && (ip == nil || !ip.IsLoopback()) {
		http.Error(w, "请通过 HTTPS 管理证书与 DNS 授权", 400)
		return
	}
	var in struct {
		Names                                                                  []string
		CA, KeyType, EABKeyID, EABHMAC, CertificatePath, PrivateKeyPath        string
		Action, Email, Provider, AccessKey, SecretKey, Certificate, PrivateKey string
		CurrentPassword                                                        string
		AutoRenew, TermsAccepted                                               bool
		RenewBeforeDays, RetryHours                                            *int
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in) != nil {
		http.Error(w, "请求格式错误或证书文件过大", 400)
		return
	}
	if domain == "" {
		http.Error(w, "请先保存主域名", 400)
		return
	}
	if in.Action == "deploy-export" {
		a.exportCertificateDeployment(w, r, m, domain, in.CurrentPassword)
		return
	}
	if in.Action == "issue" || in.Action == "renew" {
		if in.Action == "renew" {
			m.mu.Lock()
			allowed := m.state.Source == "acme" && m.pair != nil
			m.mu.Unlock()
			if !allowed {
				http.Error(w, "只有自动申请的证书支持立即续期，请重新导入手动证书", 400)
				return
			}
		}
		if err := m.start(domain); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, m.status(domain))
		return
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		http.Error(w, "证书任务正在运行，暂时不能修改配置", 409)
		return
	}
	s := m.state
	var err error
	switch in.Action {
	case "export":
		if m.pair == nil {
			err = errors.New("尚未安装证书")
			break
		}
		m.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]string{"certificate": s.CertPEM, "filename": domain + "-fullchain.pem"})
		return
	case "renewal":
		if s.Source != "acme" || m.pair == nil {
			err = errors.New("导入证书不能自动续期")
			break
		}
		if err = setRenewalPolicy(&s, in.RenewBeforeDays, in.RetryHours); err != nil {
			break
		}
		s.AutoRenew = in.AutoRenew
		message := "自动续期已关闭"
		if s.AutoRenew {
			message = "自动续期已开启"
		}
		days, hours := renewalPolicy(s)
		message += fmt.Sprintf("；提前 %d 天；失败后 %d 小时重试", days, hours)
		addCertificateEvent(&s, "续期设置", "success", message)
		err = m.saveLocked(s)
	case "configure":
		if err = setRenewalPolicy(&s, in.RenewBeforeDays, in.RetryHours); err != nil {
			break
		}
		if in.Provider != "" && s.Provider != "" && in.Provider != s.Provider {
			s.AccessKey = ""
			s.SecretKey = ""
		}
		if in.CA == "" {
			in.CA = "letsencrypt"
		}
		if in.CA != "letsencrypt" && in.CA != "zerossl" {
			err = errors.New("不支持的证书颁发机构")
			break
		}
		if in.CA != caName(s) {
			s.EABKeyID = ""
			s.EABHMAC = ""
		}
		if in.EABKeyID != "" || in.EABHMAC != "" {
			s.EABKeyID = strings.TrimSpace(in.EABKeyID)
			s.EABHMAC = strings.TrimSpace(in.EABHMAC)
		}
		if in.CA == "zerossl" && (s.EABKeyID == "" || s.EABHMAC == "") {
			err = errors.New("ZeroSSL 需要 EAB Key ID 和 HMAC Key")
			break
		}
		if in.KeyType == "" {
			in.KeyType = "2048"
		}
		if in.KeyType != "2048" && in.KeyType != "4096" && in.KeyType != "P256" {
			err = errors.New("不支持的密钥算法")
			break
		}
		if in.Names == nil {
			in.Names = []string{domain, "*." + domain}
		}
		in.Names, err = normalizeCertificateNames(in.Names, domain)
		if err != nil {
			break
		}
		s.Names = in.Names
		s.CA = in.CA
		s.KeyType = in.KeyType
		if in.Provider == "" {
			if s.Provider != "" {
				in.Provider = s.Provider
			} else {
				in.Provider = "alidns"
			}
		}
		address, e := mail.ParseAddress(strings.TrimSpace(in.Email))
		if e != nil || address.Address != strings.TrimSpace(in.Email) {
			err = errors.New("请填写有效邮箱")
			break
		}
		if in.Provider != "alidns" && in.Provider != "dnspod" && in.Provider != "cloudflare" {
			err = errors.New("请选择支持的 DNS 平台")
			break
		}
		if in.Provider == "alidns" && (in.AccessKey == "") != (in.SecretKey == "") {
			err = errors.New("AccessKey ID 和 Secret 必须一起填写")
			break
		}
		if in.AccessKey != "" {
			s.AccessKey = strings.TrimSpace(in.AccessKey)
			s.SecretKey = strings.TrimSpace(in.SecretKey)
		}
		if !in.TermsAccepted {
			err = errors.New("请先阅读并确认 ACME 服务条款")
			break
		}
		s.Domain = domain
		s.Provider = in.Provider
		s.Email = address.Address
		s.AutoRenew = in.AutoRenew
		s.TermsAccepted = in.TermsAccepted
		if s.AccessKey == "" || (s.Provider == "alidns" && s.SecretKey == "") {
			err = errors.New("请填写该 DNS 平台的授权凭据")
			break
		}
		s.PendingACME = s.PendingACME || m.pair == nil || s.Source != "acme" || acmeConfigurationChanged(m.state, s)
		addCertificateEvent(&s, "申请配置", "success", "证书申请配置已保存")
		err = m.saveLocked(s)
	case "upload", "path", "reload":
		if in.Action == "reload" {
			if s.Source != "path" {
				err = errors.New("当前证书不是从服务器路径导入")
				break
			}
			in.Action = "path"
			in.CertificatePath, in.PrivateKeyPath = s.CertificatePath, s.PrivateKeyPath
		}
		if in.Action == "path" {
			in.Certificate, in.PrivateKey, err = readCertificateFiles(in.CertificatePath, in.PrivateKeyPath)
			if err != nil {
				break
			}
		}
		var pair *tls.Certificate
		pair, _, err = validateCertificate(in.Certificate, in.PrivateKey, domain)
		if err == nil {
			s.Domain = domain
			s.CertPEM = in.Certificate
			s.KeyPEM = in.PrivateKey
			s.Source = "upload"
			if in.Action == "path" {
				s.Source = "path"
				s.CertificatePath = in.CertificatePath
				s.PrivateKeyPath = in.PrivateKeyPath
			} else {
				s.CertificatePath = ""
				s.PrivateKeyPath = ""
			}
			s.Names = append([]string{}, pair.Leaf.DNSNames...)
			s.AutoRenew = false
			s.PendingACME = false
			s.UpdatedAt = time.Now().UTC()
			s.LastResult = "证书已上传；手动证书不会自动续期"
			if in.Action == "path" {
				s.LastResult = "已从服务器文件读取并安装证书；文件更新后需重新读取"
			}
			addCertificateEvent(&s, "证书导入", "success", s.LastResult)
			err = m.saveLocked(s)
			if err == nil {
				m.pair = pair
			}
		}
	default:
		err = errors.New("不支持的操作")
	}
	if err != nil && (in.Action == "upload" || in.Action == "path" || in.Action == "reload") {
		failed := m.state
		failed.LastResult = "证书导入失败：" + err.Error()
		addCertificateEvent(&failed, "证书导入", "error", failed.LastResult)
		if saveErr := m.saveLocked(failed); saveErr != nil {
			log.Print("certificate import failure history could not be saved")
		}
	}
	m.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, m.status(domain))
}
func (a *app) startCertificateServices() {
	a.initializeHTTPS()
	go func() {
		check := func() {
			for _, d := range a.store.Domains() {
				m, e := a.certificateFor(d.BaseDomain)
				if e == nil && m.renewDue(d.BaseDomain, time.Now()) {
					_ = m.start(d.BaseDomain)
				}
			}
		}
		check()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			check()
		}
	}()
}

func (a *app) certificateFor(domain string) (*certificateManager, error) {
	if a.certs != nil {
		return a.certs, nil
	}
	if domain == "" {
		return nil, errors.New("请选择域名")
	}
	a.certMu.Lock()
	defer a.certMu.Unlock()
	if m := a.certManagers[domain]; m != nil {
		return m, nil
	}
	name := fmt.Sprintf("%x.json", sha256.Sum256([]byte(domain)))
	m, err := newCertificateManager(filepath.Join(a.certDir, name))
	if err == nil {
		a.certManagers[domain] = m
	}
	return m, err
}

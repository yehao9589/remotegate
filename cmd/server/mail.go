package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate/internal/config"
)

type mailEvents struct {
	Offline     bool `json:"offline"`
	Certificate bool `json:"certificate"`
	Update      bool `json:"update"`
	Recovery    bool `json:"recovery"`
}
type smtpSettings struct {
	Enabled    bool       `json:"enabled"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	Security   string     `json:"security"`
	Username   string     `json:"username"`
	Password   string     `json:"password"`
	From       string     `json:"from"`
	Recipients []string   `json:"recipients"`
	Events     mailEvents `json:"events"`
}
type mailDelivery struct {
	AlertID  string    `json:"alertId"`
	Recovery bool      `json:"recovery"`
	Phase    string    `json:"phase"`
	Attempts int       `json:"attempts"`
	NextAt   time.Time `json:"nextAt"`
	SentAt   time.Time `json:"sentAt,omitempty"`
}
type mailRecord struct {
	Templates     map[string]mailTemplate `json:"templates,omitempty"`
	Version       int                     `json:"version"`
	Settings      smtpSettings            `json:"settings"`
	Deliveries    map[string]mailDelivery `json:"deliveries"`
	LastSentAt    time.Time               `json:"lastSentAt,omitempty"`
	LastAttemptAt time.Time               `json:"lastAttemptAt,omitempty"`
	LastError     string                  `json:"lastError,omitempty"`
}
type notificationMailer struct {
	mu, sending sync.Mutex
	path        string
	record      mailRecord
	roots       *x509.CertPool
}

func defaultMailSettings() smtpSettings {
	return smtpSettings{Port: 465, Security: "tls", Recipients: []string{}, Events: mailEvents{true, true, true, true}}
}
func (a *app) mailService() (*notificationMailer, error) {
	a.mailMu.Lock()
	defer a.mailMu.Unlock()
	if a.mail != nil || a.mailErr != nil {
		return a.mail, a.mailErr
	}
	m := &notificationMailer{path: filepath.Join(a.store.Directory(), "mail.json"), record: mailRecord{Version: 1, Settings: defaultMailSettings(), Deliveries: map[string]mailDelivery{}}}
	raw, err := os.ReadFile(m.path)
	if err == nil {
		if len(raw) > 4<<20 || json.Unmarshal(raw, &m.record) != nil || m.record.Version != 1 {
			a.mailErr = errors.New("邮件配置损坏，请恢复 mail.json 备份")
			return nil, a.mailErr
		}
		if _, err := normalizeSMTP(m.record.Settings); err != nil {
			a.mailErr = errors.New("邮件配置无效，请恢复 mail.json 备份")
			return nil, a.mailErr
		}
		if validateMailTemplates(m.record.Templates) != nil {
			a.mailErr = errors.New("邮件模板无效，请恢复 mail.json 备份")
			return nil, a.mailErr
		}
	} else if !os.IsNotExist(err) {
		a.mailErr = errors.New("邮件配置无法读取，请检查数据目录权限")
		return nil, a.mailErr
	}
	if m.record.Deliveries == nil {
		m.record.Deliveries = map[string]mailDelivery{}
	}
	a.mail = m
	return m, nil
}
func normalizeSMTP(c smtpSettings) (smtpSettings, error) {
	c.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Host)), ".")
	c.Username = strings.TrimSpace(c.Username)
	if c.Security != "tls" && c.Security != "starttls" {
		return c, errors.New("请选择 TLS 或 STARTTLS 加密")
	}
	if c.Port < 1 || c.Port > 65535 || len(c.Host) > 253 || strings.ContainsAny(c.Host, "\r\n /\\@\x00") || len(c.Username) > 254 || strings.ContainsAny(c.Username, "\r\n\x00") || len(c.Password) > 4096 || strings.ContainsAny(c.Password, "\r\n\x00") {
		return c, errors.New("SMTP 地址、端口或账号格式无效")
	}
	if c.Host != "" && net.ParseIP(c.Host) == nil && !checkHostPattern.MatchString(c.Host) && c.Host != "localhost" {
		return c, errors.New("SMTP 服务器请填写域名或 IP，不包含协议和端口")
	}
	address := func(value string) (string, error) {
		if len(value) > 320 || strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("邮箱地址格式无效")
		}
		parsed, err := netmail.ParseAddress(strings.TrimSpace(value))
		if err != nil || !strings.Contains(parsed.Address, "@") {
			return "", errors.New("邮箱地址格式无效")
		}
		for _, r := range parsed.Address {
			if r > 127 {
				return "", errors.New("邮箱地址请使用 ASCII 格式")
			}
		}
		return parsed.Address, nil
	}
	if c.From != "" {
		value, err := address(c.From)
		if err != nil {
			return c, err
		}
		c.From = value
	}
	if len(c.Recipients) > 10 {
		return c, errors.New("最多填写 10 个收件邮箱")
	}
	recipients := []string{}
	seen := map[string]bool{}
	for _, value := range c.Recipients {
		recipient, err := address(value)
		if err != nil {
			return c, err
		}
		if !seen[strings.ToLower(recipient)] {
			recipients = append(recipients, recipient)
			seen[strings.ToLower(recipient)] = true
		}
	}
	c.Recipients = recipients
	if c.Enabled && (c.Host == "" || c.From == "" || len(c.Recipients) == 0 || c.Username != "" && c.Password == "") {
		return c, errors.New("启用前请填写 SMTP 服务器、发件邮箱、收件邮箱及所需授权码")
	}
	if c.Password != "" && c.Username == "" {
		return c, errors.New("填写 SMTP 授权码时也需填写认证账号")
	}
	return c, nil
}
func (m *notificationMailer) saveLocked() error {
	raw, err := json.MarshalIndent(m.record, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".mail-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), m.path)
	}
	return err
}
func (m *notificationMailer) view() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.record.Settings
	failed := 0
	for _, d := range m.record.Deliveries {
		if d.Phase == "failed" {
			failed++
		}
	}
	return map[string]any{"enabled": c.Enabled, "host": c.Host, "port": c.Port, "security": c.Security, "username": c.Username, "passwordSet": c.Password != "", "from": c.From, "recipients": append([]string{}, c.Recipients...), "events": c.Events, "lastSentAt": m.record.LastSentAt, "lastAttemptAt": m.record.LastAttemptAt, "lastError": m.record.LastError, "failed": failed}
}
func (a *app) mailHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !sameAuthOrigin(r) {
		http.Error(w, "请求来源不匹配", 403)
		return
	}
	m, err := a.mailService()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if r.Method == "GET" {
		writeJSON(w, m.view())
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct {
		Action        string       `json:"action"`
		Settings      smtpSettings `json:"settings"`
		ClearPassword bool         `json:"clearPassword"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "邮件设置格式无效", 400)
		return
	}
	switch input.Action {
	case "save":
		m.mu.Lock()
		old := m.record.Settings
		if input.Settings.Password == "" && !input.ClearPassword {
			input.Settings.Password = old.Password
		}
		settings, err := normalizeSMTP(input.Settings)
		if err != nil {
			m.mu.Unlock()
			http.Error(w, err.Error(), 400)
			return
		}
		m.record.Settings = settings
		if err := m.saveLocked(); err != nil {
			m.record.Settings = old
			m.mu.Unlock()
			http.Error(w, "邮件设置保存失败，请检查数据目录", 500)
			return
		}
		m.mu.Unlock()
		writeJSON(w, m.view())
	case "test":
		if !m.sending.TryLock() {
			http.Error(w, "邮件正在发送，请稍后重试", 409)
			return
		}
		defer m.sending.Unlock()
		m.mu.Lock()
		settings := m.record.Settings
		testTemplate, customTest := m.record.Templates["test"]
		m.mu.Unlock()
		check := settings
		check.Enabled = true
		if _, err := normalizeSMTP(check); err != nil {
			http.Error(w, "请先保存完整的 SMTP 与收件邮箱设置", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		subject, body := mailTestSubject, mailTestBody
		if customTest {
			subject, body = renderMailTemplate("test", testTemplate, mailItem{alert: config.Alert{Kind: "test", Title: mailTestSubject, Detail: mailTestBody, CreatedAt: time.Now()}}, nil, time.Now(), 1)
		}
		if err := sendSMTP(ctx, settings, subject, body, m.roots); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, map[string]any{"message": "测试邮件已交给 SMTP 服务器，请检查收件箱或垃圾邮件"})
	case "retry":
		m.mu.Lock()
		old := m.record.Deliveries
		next := map[string]mailDelivery{}
		for key, value := range old {
			if value.Phase == "failed" {
				value.Attempts = 0
				value.NextAt = time.Time{}
				value.Phase = "retry"
			}
			next[key] = value
		}
		m.record.Deliveries = next
		if err := m.saveLocked(); err != nil {
			m.record.Deliveries = old
			m.mu.Unlock()
			http.Error(w, "重试状态保存失败", 500)
			return
		}
		m.mu.Unlock()
		writeJSON(w, map[string]any{"message": "失败通知已重新排队，将在下次后台检查时重试"})
	default:
		http.Error(w, "邮件操作无效", 400)
	}
}

// Never surface untrusted SMTP reply text: it can echo credentials or arbitrary
// server content. Only a fixed stage and, when available, a numeric reply code.
func smtpFailure(stage string, err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 100 && reply.Code <= 599 {
		return fmt.Errorf("%s（SMTP %d）", stage, reply.Code)
	}
	return errors.New(stage)
}

type smtpLoginAuth struct {
	username, password string
	step               int
}

func (a *smtpLoginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("SMTP 认证需要 TLS")
	}
	a.step = 0
	return "LOGIN", nil, nil
}
func (a *smtpLoginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	default:
		return nil, errors.New("SMTP LOGIN 交互无效")
	}
}

func sendSMTP(ctx context.Context, c smtpSettings, subject, body string, roots *x509.CertPool) error {
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return errors.New("无法连接 SMTP 服务器，请核对地址、端口及网络")
	}
	defer connection.Close()
	rawConnection := connection
	stop := context.AfterFunc(ctx, func() { _ = rawConnection.Close() })
	defer stop()
	deadline := time.Now().Add(20 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	if c.Security == "tls" {
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS 验证失败，请检查服务器证书、加密方式与系统时间")
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, c.Host)
	if err != nil {
		return smtpFailure("SMTP 服务器未正确响应", err)
	}
	defer client.Close()
	if err := client.Hello("remotegate.local"); err != nil {
		return smtpFailure("SMTP 握手失败", err)
	}
	if c.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 服务器不支持 STARTTLS，已停止发送，未提交认证信息")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP STARTTLS 验证失败，请检查证书与加密设置")
		}
	}
	if _, ok := client.TLSConnectionState(); !ok {
		return errors.New("SMTP 连接未加密，已停止发送")
	}
	if c.Username != "" {
		ok, methods := client.Extension("AUTH")
		if !ok {
			return errors.New("SMTP 服务器不支持认证")
		}
		var auth smtp.Auth
		for _, method := range strings.Fields(strings.ToUpper(methods)) {
			if method == "PLAIN" {
				auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
				break
			}
			if method == "LOGIN" {
				auth = &smtpLoginAuth{username: c.Username, password: c.Password}
			}
		}
		if auth == nil {
			return errors.New("SMTP 服务器未提供 PLAIN / LOGIN 认证")
		}
		if err := client.Auth(auth); err != nil {
			return smtpFailure("SMTP 认证失败，请核对账号与邮箱授权码", err)
		}
	}
	if err := client.Mail(c.From); err != nil {
		return smtpFailure("发件邮箱被拒绝，请核对发件人设置", err)
	}
	for _, recipient := range c.Recipients {
		if err := client.Rcpt(recipient); err != nil {
			return smtpFailure("收件邮箱被拒绝，请核对收件人设置", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return smtpFailure("SMTP 服务器拒绝邮件正文", err)
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: undisclosed-recipients:;\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@remotegate.local>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", (&netmail.Address{Name: "RemoteGate", Address: c.From}).String(), mime.QEncoding.Encode("utf-8", subject), time.Now().UTC().Format(time.RFC1123Z), makeID())
	encoded := quotedprintable.NewWriter(&message)
	_, _ = encoded.Write([]byte(body))
	_ = encoded.Close()
	if _, err = writer.Write(message.Bytes()); err != nil {
		return smtpFailure("邮件发送中断，请检查网络", err)
	}
	if err = writer.Close(); err != nil {
		return smtpFailure("SMTP 服务器未确认收信", err)
	}
	// DATA success is delivery acceptance. A failed QUIT must not resend a mail
	// which the SMTP server has already accepted.
	_ = client.Quit()
	return nil
}

type mailItem struct {
	key      string
	alert    config.Alert
	recovery bool
}

// Keep the current event and the last successful opening for every retained
// alert before pruning old severity changes. Those records are needed for
// deduplication and for deciding whether a recovery email is appropriate.
func boundedMailDeliveries(all map[string]mailDelivery, alerts []config.Alert) map[string]mailDelivery {
	if len(all) <= 2000 {
		return all
	}
	needed := map[string]bool{}
	opened := map[string]string{}
	for _, a := range alerts {
		needed[mailEventKey(a)] = true
	}
	for key, d := range all {
		if !d.Recovery && d.Phase == "sent" {
			previous, ok := opened[d.AlertID]
			if !ok || all[previous].SentAt.Before(d.SentAt) {
				opened[d.AlertID] = key
			}
		}
	}
	for _, key := range opened {
		needed[key] = true
	}
	keys := make([]string, 0, len(all))
	for key := range all {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if needed[keys[i]] != needed[keys[j]] {
			return needed[keys[i]]
		}
		left, right := all[keys[i]].NextAt, all[keys[j]].NextAt
		if left.Equal(right) {
			return keys[i] < keys[j]
		}
		return left.After(right)
	})
	kept := map[string]mailDelivery{}
	for _, key := range keys[:2000] {
		kept[key] = all[key]
	}
	return kept
}

func mailEventKey(a config.Alert) string {
	raw, _ := json.Marshal([]any{a.ID, a.Title, a.Detail, a.ResolvedAt})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (m *notificationMailer) dueLocked(alerts []config.Alert, now time.Time) []mailItem {
	c := m.record.Settings
	if !c.Enabled {
		return nil
	}
	items := []mailItem{}
	opened := map[string]bool{}
	for _, d := range m.record.Deliveries {
		if !d.Recovery && d.Phase == "sent" {
			opened[d.AlertID] = true
		}
	}
	for _, a := range alerts {
		if a.Kind == "offline" && !c.Events.Offline || a.Kind == "certificate" && !c.Events.Certificate || a.Kind == "update" && !c.Events.Update {
			continue
		}
		recovery := a.ResolvedAt != nil
		if recovery && (!c.Events.Recovery || !opened[a.ID]) {
			continue
		}
		key := mailEventKey(a)
		delivery, exists := m.record.Deliveries[key]
		if exists && (delivery.Phase == "sent" || delivery.Attempts >= 5 || now.Before(delivery.NextAt)) {
			continue
		}
		items = append(items, mailItem{key, a, recovery})
	}
	return items
}
func (m *notificationMailer) deliver(ctx context.Context, alerts []config.Alert, now time.Time, deviceNames ...map[string]string) {
	if !m.sending.TryLock() {
		return
	}
	defer m.sending.Unlock()
	m.mu.Lock()
	items := m.dueLocked(alerts, now)
	if len(items) == 0 {
		m.mu.Unlock()
		return
	}
	settings := m.record.Settings
	templates := m.record.Templates
	keep := map[string]bool{}
	for _, a := range alerts {
		keep[a.ID] = true
	}
	old := m.record.Deliveries
	next := map[string]mailDelivery{}
	for key, d := range old {
		if keep[d.AlertID] {
			next[key] = d
		}
	}
	for _, item := range items {
		d := next[item.key]
		d.AlertID = item.alert.ID
		d.Recovery = item.recovery
		d.Attempts++
		d.Phase = "sending"
		delay := time.Minute * time.Duration(1<<min(d.Attempts-1, 6))
		d.NextAt = now.Add(delay)
		next[item.key] = d
	}
	m.record.Deliveries = boundedMailDeliveries(next, alerts)
	m.record.LastAttemptAt = now.UTC()
	if err := m.saveLocked(); err != nil {
		m.record.Deliveries = old
		m.record.LastError = "通知记录保存失败，未发送邮件"
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	names := map[string]string{}
	if len(deviceNames) > 0 {
		names = deviceNames[0]
	}
	subject, body := composeConfiguredMail(items, templates, names, now)
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := sendSMTP(sendCtx, settings, subject, body, m.roots)
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range items {
		d := m.record.Deliveries[item.key]
		if err == nil {
			d.Phase = "sent"
			d.SentAt = time.Now().UTC()
		} else {
			d.Phase = "retry"
			if d.Attempts >= 5 {
				d.Phase = "failed"
			}
		}
		m.record.Deliveries[item.key] = d
	}
	if err == nil {
		m.record.LastSentAt = time.Now().UTC()
		m.record.LastError = ""
	} else {
		m.record.LastError = err.Error()
	}
	if err := m.saveLocked(); err != nil {
		m.record.LastError = "邮件发送结果未能保存；请检查数据目录，重启后可能重试"
	}
}
func (a *app) maintainMail(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if m, err := a.mailService(); err == nil {
				m.deliver(ctx, a.store.Alerts(), now, mailDeviceNames(a.store.Snapshot()))
			}
		}
	}
}

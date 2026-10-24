package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

type smtpFixture struct {
	listener              net.Listener
	roots                 *x509.CertPool
	pair                  tls.Certificate
	port                  int
	mode                  string
	loginOnly, rejectAuth bool
	mu                    sync.Mutex
	messages              [][]byte
	auths, insecureAuths  int
	workers               sync.WaitGroup
	done                  chan struct{}
}

func newSMTPFixture(t *testing.T, mode string) *smtpFixture {
	t.Helper()
	certificate, key := testCertificate(t, "localhost", time.Now().Add(time.Hour))
	pair, err := tls.X509KeyPair([]byte(certificate), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	f := &smtpFixture{listener: listener, roots: x509.NewCertPool(), pair: pair, port: number, mode: mode, done: make(chan struct{})}
	f.roots.AppendCertsFromPEM([]byte(certificate))
	go func() {
		defer close(f.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.workers.Add(1)
			go func() { defer f.workers.Done(); f.serve(conn) }()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-f.done; f.workers.Wait() })
	return f
}
func (f *smtpFixture) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	secure := f.mode == "tls"
	if secure {
		conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.pair}, MinVersion: tls.VersionTLS12})
	}
	tp := textproto.NewConn(conn)
	if tp.PrintfLine("220 localhost test SMTP") != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch command {
		case "EHLO":
			tp.PrintfLine("250-localhost")
			if !secure && f.mode == "starttls" {
				tp.PrintfLine("250-STARTTLS")
			}
			methods := "PLAIN LOGIN"
			f.mu.Lock()
			loginOnly := f.loginOnly
			f.mu.Unlock()
			if loginOnly {
				methods = "LOGIN"
			}
			tp.PrintfLine("250-AUTH %s", methods)
			tp.PrintfLine("250 SIZE 1000000")
		case "HELO":
			tp.PrintfLine("250 localhost")
		case "STARTTLS":
			if f.mode != "starttls" {
				tp.PrintfLine("502 unavailable")
				continue
			}
			tp.PrintfLine("220 begin TLS")
			wrapped := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.pair}, MinVersion: tls.VersionTLS12})
			if wrapped.Handshake() != nil {
				return
			}
			conn = wrapped
			tp = textproto.NewConn(conn)
			secure = true
		case "AUTH":
			f.mu.Lock()
			f.auths++
			if !secure {
				f.insecureAuths++
			}
			f.mu.Unlock()
			f.mu.Lock()
			reject := f.rejectAuth
			f.mu.Unlock()
			if reject {
				tp.PrintfLine("535 smtp-test-secret must never reach the browser")
				continue
			}
			if strings.HasPrefix(strings.ToUpper(line), "AUTH LOGIN") {
				tp.PrintfLine("334 VXNlcm5hbWU6")
				if _, err := tp.ReadLine(); err != nil {
					return
				}
				tp.PrintfLine("334 UGFzc3dvcmQ6")
				if _, err := tp.ReadLine(); err != nil {
					return
				}
			}
			tp.PrintfLine("235 authenticated")
		case "MAIL", "RCPT", "RSET":
			tp.PrintfLine("250 OK")
		case "DATA":
			tp.PrintfLine("354 continue")
			body, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.messages = append(f.messages, body)
			f.mu.Unlock()
			tp.PrintfLine("250 accepted")
		case "QUIT":
			return // Simulate a dropped QUIT reply after DATA acceptance.
		default:
			tp.PrintfLine("502 unsupported")
		}
	}
}
func (f *smtpFixture) settings() smtpSettings {
	return smtpSettings{Enabled: true, Host: "localhost", Port: f.port, Security: f.mode, Username: "smtp-user", Password: "smtp-test-secret", From: "from@example.com", Recipients: []string{"first@example.com", "second@example.com"}, Events: mailEvents{true, true, true, true}}
}
func (f *smtpFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.messages) }

func TestSMTPRequiresVerifiedEncryptionAndSupportsTLSAndSTARTTLS(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			f := newSMTPFixture(t, mode)
			settings := f.settings()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := sendSMTP(ctx, settings, "中文测试", "测试正文，不含凭据", f.roots); err != nil {
				t.Fatal(err)
			}
			if f.count() != 1 {
				t.Fatal("mail not received")
			}
			f.mu.Lock()
			raw := append([]byte{}, f.messages[0]...)
			insecure := f.insecureAuths
			f.mu.Unlock()
			if insecure != 0 {
				t.Fatal("credentials sent without TLS")
			}
			message, err := netmail.ReadMessage(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			subject, _ := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			body, _ := io.ReadAll(quotedprintable.NewReader(message.Body))
			if subject != "中文测试" || !strings.Contains(string(body), "测试正文") || strings.Contains(string(raw), "smtp-test-secret") {
				t.Fatal("mail encoding or privacy failed")
			}
			if message.Header.Get("To") != "undisclosed-recipients:;" {
				t.Fatal("recipient privacy missing")
			}
		})
	}
}
func TestSMTPRejectsUntrustedTLSAndSTARTTLSDowngradeBeforeAuthentication(t *testing.T) {
	f := newSMTPFixture(t, "tls")
	settings := f.settings()
	if err := sendSMTP(context.Background(), settings, "test", "test", nil); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if f.count() != 0 {
		t.Fatal("mail sent despite invalid certificate")
	}
	plain := newSMTPFixture(t, "none")
	settings = plain.settings()
	settings.Security = "starttls"
	err := sendSMTP(context.Background(), settings, "test", "test", nil)
	if err == nil || strings.Contains(err.Error(), settings.Password) {
		t.Fatal("downgrade not rejected safely")
	}
	plain.mu.Lock()
	auths := plain.auths
	plain.mu.Unlock()
	if auths != 0 {
		t.Fatal("SMTP credentials submitted without STARTTLS")
	}
}
func TestSMTPLoginFallbackAndUntrustedReplyRedaction(t *testing.T) {
	f := newSMTPFixture(t, "tls")
	f.mu.Lock()
	f.loginOnly = true
	f.mu.Unlock()
	settings := f.settings()
	if err := sendSMTP(context.Background(), settings, "test", "test", f.roots); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rejectAuth = true
	f.mu.Unlock()
	err := sendSMTP(context.Background(), settings, "test", "test", f.roots)
	if err == nil || strings.Contains(err.Error(), settings.Password) || !strings.Contains(err.Error(), "535") {
		t.Fatal("unsafe authentication error")
	}
}

func TestMailDigestDeduplicatesAcrossRestartAndSendsRecoveryOnlyAfterOpening(t *testing.T) {
	a := freshAuthApp(t)
	m, err := a.mailService()
	if err != nil {
		t.Fatal(err)
	}
	server := newSMTPFixture(t, "tls")
	m.record.Settings = server.settings()
	m.roots = server.roots
	now := time.Now()
	alerts := []config.Alert{{ID: "one", Kind: "offline", Title: "路由器离线", Detail: "持续离线", CreatedAt: now}, {ID: "two", Kind: "update", Title: "更新失败", CreatedAt: now}}
	m.deliver(context.Background(), alerts, now)
	if server.count() != 1 {
		t.Fatal("alerts not grouped into one digest")
	}
	m.deliver(context.Background(), alerts, now.Add(time.Minute))
	if server.count() != 1 {
		t.Fatal("unchanged incidents sent twice")
	}
	a.mail = nil
	restored, err := a.mailService()
	if err != nil {
		t.Fatal(err)
	}
	restored.roots = server.roots
	restored.deliver(context.Background(), alerts, now.Add(2*time.Minute))
	if server.count() != 1 {
		t.Fatal("restart sent duplicate mail")
	}
	resolved := now.Add(time.Minute)
	alerts[0].ResolvedAt = &resolved
	alerts = append(alerts, config.Alert{ID: "never-opened", Kind: "offline", ResolvedAt: &resolved})
	restored.deliver(context.Background(), alerts, now.Add(3*time.Minute))
	if server.count() != 2 {
		t.Fatal("recovery digest missing")
	}
	restored.deliver(context.Background(), alerts, now.Add(4*time.Minute))
	if server.count() != 2 {
		t.Fatal("recovery repeated")
	}
}
func TestMailBackoffStopsAfterFiveFailuresAndDisabledSettingsSendNothing(t *testing.T) {
	a := freshAuthApp(t)
	m, _ := a.mailService()
	server := newSMTPFixture(t, "tls")
	server.mu.Lock()
	server.rejectAuth = true
	server.mu.Unlock()
	m.record.Settings = server.settings()
	m.roots = server.roots
	now := time.Now()
	alerts := []config.Alert{{ID: "one", Kind: "offline", Title: "Router offline", CreatedAt: now}}
	for i := 0; i < 8; i++ {
		m.deliver(context.Background(), alerts, now.Add(time.Duration(i)*time.Hour))
	}
	key := mailEventKey(alerts[0])
	if m.record.Deliveries[key].Attempts != 5 || m.record.Deliveries[key].Phase != "failed" {
		t.Fatal("retry unbounded")
	}
	if strings.Contains(m.record.LastError, "smtp-test-secret") {
		t.Fatal("SMTP reply leaked secret")
	}
	m.record.Settings.Enabled = false
	before := len(m.record.Deliveries)
	m.deliver(context.Background(), []config.Alert{{ID: "new", Kind: "offline"}}, now)
	if len(m.record.Deliveries) != before {
		t.Fatal("disabled notifier queued mail")
	}
}

func TestMailSettingsOnlyAdminCanReadAndPasswordsNeverReturned(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	settings := defaultMailSettings()
	settings.Username = "sender@example.com"
	settings.Password = "saved-smtp-secret"
	settings.Host = "smtp.example.com"
	settings.From = "sender@example.com"
	settings.Recipients = []string{"owner@example.com"}
	raw, _ := json.Marshal(map[string]any{"action": "save", "settings": settings})
	result := authCall(a, "/api/admin/mail", string(raw), "", cookie)
	requireStatus(t, result, 200)
	for _, path := range []string{"/api/admin/mail"} {
		requireStatus(t, authCall(a, path, "", "", nil), 401)
		result = authCall(a, path, "", "", cookie)
		requireStatus(t, result, 200)
		if strings.Contains(result.Body.String(), settings.Password) || strings.Contains(result.Body.String(), `"password":`) {
			t.Fatal("SMTP secret returned to browser")
		}
	}
	settings.Password = ""
	settings.Port = 587
	settings.Security = "starttls"
	raw, _ = json.Marshal(map[string]any{"action": "save", "settings": settings})
	requireStatus(t, authCall(a, "/api/admin/mail", string(raw), "", cookie), 200)
	if a.mail.record.Settings.Password != "saved-smtp-secret" {
		t.Fatal("blank edit discarded stored secret")
	}
	file, err := os.Stat(a.mail.path)
	if err != nil || file.Size() == 0 {
		t.Fatal("mail settings not persisted")
	}
	if _, err := normalizeSMTP(smtpSettings{Enabled: true, Security: "plaintext", Port: 25}); err == nil {
		t.Fatal("plaintext mode accepted")
	}
	settings = a.mail.record.Settings
	settings.From = "valid@example.com\r\nBcc: attacker@example.com"
	if _, err := normalizeSMTP(settings); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestMailDeliveryRetentionKeepsCurrentEventAndRecoveryEvidence(t *testing.T) {
	now := time.Now()
	alert := config.Alert{ID: "active", Title: "current", Kind: "offline", CreatedAt: now}
	records := map[string]mailDelivery{}
	for i := 0; i < 2500; i++ {
		records[strconv.Itoa(i)] = mailDelivery{AlertID: "active", Phase: "sent", NextAt: now.Add(time.Duration(i) * time.Minute), SentAt: now.Add(time.Duration(i) * time.Minute)}
	}
	current := mailEventKey(alert)
	records[current] = mailDelivery{AlertID: alert.ID, Phase: "failed", Attempts: 5, NextAt: now}
	kept := boundedMailDeliveries(records, []config.Alert{alert})
	if len(kept) != 2000 || kept[current].Phase != "failed" || kept["2499"].Phase != "sent" {
		t.Fatal("retention discarded deduplication or recovery evidence")
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/local/remotegate/internal/config"
)

func TestMailTemplateValidationAndSinglePassVariables(t *testing.T) {
	for _, template := range []mailTemplate{{"", "body"}, {"title", " "}, {"title\nBcc: bad", "body"}, {"{{unknown}}", "body"}, {"title", "{{ bad"}, {"title", "body\x00"}, {strings.Repeat("字", 201), "body"}, {"title", strings.Repeat("字", 10001)}} {
		if _, err := normalizeMailTemplate(template); err == nil {
			t.Fatal("accepted invalid template", template.Subject)
		}
	}
	tpl, err := normalizeMailTemplate(mailTemplate{"{{device_name}} · {{status}}", "{{detail}}\n{{domain}}\n{{resolved_at}}"})
	if err != nil {
		t.Fatal(err)
	}
	subject, body := renderMailTemplate("offline", tpl, mailItem{alert: config.Alert{DeviceID: "one", Detail: "literal {{device_id}} <script>"}}, map[string]string{"one": "Home\nBcc: no"}, time.Now(), 1)
	if strings.ContainsAny(subject, "\r\n") || !strings.Contains(subject, "Home Bcc: no") || !strings.Contains(body, "literal {{device_id}} <script>") || !strings.Contains(body, "\r\n—") {
		t.Fatal(subject, body)
	}
}

func TestMailTemplatesPersistPreviewAndResetWithoutChangingSMTP(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	initial := authCall(a, "/api/admin/mail/templates", "", "", cookie)
	requireStatus(t, initial, 200)
	if !strings.Contains(initial.Body.String(), "device_name") {
		t.Fatal("missing variable catalog")
	}
	tpl := mailTemplate{"我的设备 · {{device_name}}", "{{device_name}} / {{device_id}}\n{{title}}\n{{detail}}\n{{time}}"}
	raw, _ := json.Marshal(map[string]any{"kind": "offline", "template": tpl})
	draft := authCall(a, "/api/admin/mail/preview", string(raw), "", cookie)
	requireStatus(t, draft, 200)
	if !strings.Contains(draft.Body.String(), "我的设备 · 家中路由器") {
		t.Fatal(draft.Body.String())
	}
	m, _ := a.mailService()
	if len(m.record.Templates) != 0 {
		t.Fatal("draft was saved")
	}
	if _, err := os.Stat(m.path); !os.IsNotExist(err) {
		t.Fatal("draft wrote disk", err)
	}
	raw, _ = json.Marshal(map[string]any{"action": "save", "kind": "offline", "template": tpl})
	requireStatus(t, authCall(a, "/api/admin/mail/templates", string(raw), "", cookie), 200)
	if m.record.Settings.Enabled || m.record.Settings.Password != "" || len(m.record.Deliveries) != 0 {
		t.Fatal("template changed SMTP or sent notifications")
	}
	a.mail = nil
	restored, err := a.mailService()
	if err != nil {
		t.Fatal(err)
	}
	if restored.record.Templates["offline"] != tpl {
		t.Fatal("template lost on restart")
	}
	preview := authCall(a, "/api/admin/mail/preview?kind=offline", "", "", cookie)
	requireStatus(t, preview, 200)
	if !strings.Contains(preview.Body.String(), "我的设备 · 家中路由器") {
		t.Fatal(preview.Body.String())
	}
	requireStatus(t, authCall(a, "/api/admin/mail/templates", string(raw), "https://untrusted.example", cookie), 403)
	createRegular(t, a, "guest", nil)
	member := regularCookie(t, a, "guest")
	requireStatus(t, authCall(a, "/api/admin/mail/templates", "", "", member), 401)
	requireStatus(t, authCall(a, "/api/admin/mail/templates", `{"action":"reset","kind":"offline"}`, "", cookie), 200)
	if len(restored.record.Templates) != 0 {
		t.Fatal("reset did not restore default")
	}
	requireStatus(t, authCall(a, "/api/admin/mail/templates", `{"action":"save","kind":"offline","template":{"subject":"bad {{secret}}","body":"body"}}`, "", cookie), 400)
	oldPath := restored.path
	restored.path = t.TempDir() // Atomic replacement over a directory must fail.
	requireStatus(t, authCall(a, "/api/admin/mail/templates", string(raw), "", cookie), 500)
	restored.path = oldPath
	if len(restored.record.Templates) != 0 {
		t.Fatal("failed save changed active templates")
	}
}

func TestMailCustomTemplatesAreUsedByRealSMTPAndRecovery(t *testing.T) {
	a := freshAuthApp(t)
	m, err := a.mailService()
	if err != nil {
		t.Fatal(err)
	}
	server := newSMTPFixture(t, "tls")
	m.record.Settings = server.settings()
	m.roots = server.roots
	m.record.Templates = map[string]mailTemplate{"offline": {"故障 · {{device_name}}", "设备 {{device_name}}：{{detail}}"}, "recovery": {"恢复 · {{device_name}}", "{{status}} / {{resolved_at}}"}, "test": {"自定义测试", "当前时间 {{time}}"}}
	now := time.Now()
	alerts := []config.Alert{{ID: "one", DeviceID: "router", Kind: "offline", Title: "路由器离线", Detail: "断开了", CreatedAt: now}}
	m.deliver(context.Background(), alerts, now, map[string]string{"router": "家中设备"})
	resolved := now.Add(time.Minute)
	alerts[0].ResolvedAt = &resolved
	m.deliver(context.Background(), alerts, resolved, map[string]string{"router": "家中设备"})
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	requireStatus(t, authCall(a, "/api/admin/mail", `{"action":"test"}`, "", login.Result().Cookies()[0]), 200)
	server.mu.Lock()
	messages := append([][]byte(nil), server.messages...)
	server.mu.Unlock()
	if len(messages) != 3 {
		t.Fatal("wrong delivery count", len(messages))
	}
	for i, want := range []string{"故障 · 家中设备", "恢复 · 家中设备", "自定义测试"} {
		message, err := netmail.ReadMessage(bytes.NewReader(messages[i]))
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
		body, _ := io.ReadAll(quotedprintable.NewReader(message.Body))
		if subject != want || len(body) == 0 || strings.Contains(string(body), "{{") {
			t.Fatal(subject, string(body))
		}
	}
}

func TestMailCustomDigestKeepsBatchingAndDefaultDelivery(t *testing.T) {
	now := time.Now()
	items := []mailItem{{alert: config.Alert{Kind: "offline", DeviceID: "one", Title: "first", CreatedAt: now}}, {alert: config.Alert{Kind: "update", Title: "second", CreatedAt: now}}}
	legacySubject, legacyBody := composeMailDigest(items)
	subject, body := composeConfiguredMail(items, nil, nil, now)
	if subject != legacySubject || body != legacyBody {
		t.Fatal("legacy digest changed")
	}
	subject, body = composeConfiguredMail(items, map[string]mailTemplate{"offline": {"{{device_name}}", "自定义 {{title}}"}}, map[string]string{"one": "router"}, now)
	if subject != legacySubject || !strings.Contains(body, "自定义 first") || !strings.Contains(body, "second") {
		t.Fatal(subject, body)
	}
}

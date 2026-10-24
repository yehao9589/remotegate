package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
)

const mailTestSubject = "RemoteGate 邮件通知测试"
const mailTestBody = "这是一封 RemoteGate 测试邮件。收到此邮件表示 SMTP 配置可用。\r\n\r\n设备异常提醒需在后台启用邮件通知后才会自动发送。"

// Shared by previews and SMTP delivery, so their subjects and text cannot drift.
func composeMailDigest(items []mailItem) (string, string) {
	var body strings.Builder
	body.WriteString("RemoteGate 运行通知\r\n\r\n")
	for _, item := range items {
		label := "待处理"
		if item.recovery {
			label = "已恢复 / 条件解除"
		}
		fmt.Fprintf(&body, "【%s】%s\r\n%s\r\n首次记录：%s\r\n", label, item.alert.Title, item.alert.Detail, item.alert.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
		if item.recovery && item.alert.ResolvedAt != nil {
			fmt.Fprintf(&body, "解除时间：%s\r\n", item.alert.ResolvedAt.UTC().Format("2006-01-02 15:04 UTC"))
		}
		body.WriteString("\r\n")
	}
	body.WriteString("请登录自己的 RemoteGate 后台查看详情。证书结果以最近检测时间为准；删除配置也可能解除提醒。\r\n")
	return fmt.Sprintf("RemoteGate：%d 项运行通知", len(items)), body.String()
}

func exampleMailItem(kind string, now time.Time) (mailItem, bool) {
	if kind == "test" {
		return mailItem{alert: config.Alert{Kind: "test", Title: mailTestSubject, Detail: mailTestBody, CreatedAt: now}}, true
	}
	alert := config.Alert{ID: "preview-only", DeviceID: "preview-device", Kind: kind, CreatedAt: now.Add(-5 * time.Minute)}
	switch kind {
	case "offline":
		alert.Title = "家中路由器持续离线"
		alert.Detail = "已连续离线超过 3 分钟，请检查路由器网络及插件连接状态。"
	case "certificate":
		alert.DeviceID = ""
		alert.Key = "certificate:router.example.com:443"
		alert.Title = "router.example.com 证书即将到期"
		alert.Detail = "最近检测到证书剩余 14 天，请及时检查证书续期与公网入口。"
	case "update":
		alert.Title = "家中路由器插件更新失败"
		alert.Detail = "新版未通过连接与心跳验收，已恢复旧插件，请检查设备日志后再试。"
	case "recovery":
		alert.Kind = "offline"
		alert.Title = "家中路由器持续离线"
		alert.Detail = "已连续离线超过 3 分钟，请检查路由器网络及插件连接状态。"
		resolved := now
		alert.ResolvedAt = &resolved
	default:
		return mailItem{}, false
	}
	return mailItem{alert: alert, recovery: kind == "recovery"}, true
}
func mailExample(kind string, now time.Time) (subject, body string, ok bool) {
	item, ok := exampleMailItem(kind, now)
	if !ok {
		return "", "", false
	}
	if kind == "test" {
		return mailTestSubject, mailTestBody, true
	}
	subject, body = composeMailDigest([]mailItem{item})
	return subject, body, true
}

func (a *app) mailPreviewHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	kind := r.URL.Query().Get("kind")
	var draft *mailTemplate
	if r.Method == "POST" {
		if !sameAuthOrigin(r) {
			http.Error(w, "请求来源不匹配", 403)
			return
		}
		var input struct {
			Kind     string        `json:"kind"`
			Template *mailTemplate `json:"template"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "预览参数无效", 400)
			return
		}
		kind, draft = input.Kind, input.Template
	}
	if kind == "" {
		kind = "offline"
	}
	now := time.Now().UTC().Truncate(time.Minute)
	item, ok := exampleMailItem(kind, now)
	if !ok {
		http.Error(w, "邮件预览类型无效", 400)
		return
	}
	m, err := a.mailService()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	m.mu.Lock()
	from := m.record.Settings.From
	recipients := len(m.record.Settings.Recipients)
	t, custom := m.record.Templates[kind]
	if !custom {
		t = defaultMailTemplate(kind)
	}
	m.mu.Unlock()
	if draft != nil {
		var err error
		t, err = normalizeMailTemplate(*draft)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	subject, body := renderMailTemplate(kind, t, item, map[string]string{"preview-device": "家中路由器"}, now, 1)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"kind": kind, "subject": subject, "body": body, "from": from, "recipientCount": recipients, "example": true, "generatedAt": now, "custom": custom, "draft": draft != nil})
}

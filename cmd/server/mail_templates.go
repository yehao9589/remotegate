package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/local/remotegate/internal/config"
)

type mailTemplate struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}
type mailVariable struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

var mailVariables = []mailVariable{{"device_name", "设备名称"}, {"device_id", "设备 ID"}, {"domain", "域名"}, {"title", "异常标题"}, {"detail", "异常详情"}, {"status", "当前状态"}, {"event_type", "通知类型"}, {"created_at", "首次记录时间"}, {"resolved_at", "恢复时间"}, {"time", "生成时间"}, {"count", "本次通知数量"}}
var mailPlaceholder = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

func validMailKind(kind string) bool {
	switch kind {
	case "offline", "certificate", "update", "recovery", "test":
		return true
	}
	return false
}
func defaultMailTemplate(kind string) mailTemplate {
	if kind == "test" {
		return mailTemplate{mailTestSubject, strings.ReplaceAll(mailTestBody, "\r\n", "\n")}
	}
	body := "RemoteGate 运行通知\n\n【{{status}}】{{title}}\n{{detail}}\n首次记录：{{created_at}}\n"
	if kind == "recovery" {
		body += "解除时间：{{resolved_at}}\n"
	}
	body += "\n请登录自己的 RemoteGate 后台查看详情。证书结果以最近检测时间为准；删除配置也可能解除提醒。\n"
	return mailTemplate{"RemoteGate：{{count}} 项运行通知", body}
}
func normalizeMailTemplate(t mailTemplate) (mailTemplate, error) {
	t.Subject = strings.TrimSpace(t.Subject)
	t.Body = strings.ReplaceAll(strings.ReplaceAll(t.Body, "\r\n", "\n"), "\r", "\n")
	if !utf8.ValidString(t.Subject) || !utf8.ValidString(t.Body) || t.Subject == "" || strings.TrimSpace(t.Body) == "" || utf8.RuneCountInString(t.Subject) > 200 || utf8.RuneCountInString(t.Body) > 10000 {
		return t, errors.New("邮件标题需为 1–200 字，正文需为 1–10000 字")
	}
	if strings.ContainsAny(t.Subject, "\r\n") {
		return t, errors.New("邮件标题不能换行")
	}
	for _, field := range []string{t.Subject, t.Body} {
		for _, r := range field {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return t, errors.New("模板包含无效控制字符")
			}
		}
		remaining := mailPlaceholder.ReplaceAllStringFunc(field, func(token string) string { return "" })
		if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
			return t, errors.New("变量格式应为 {{变量名}}，请使用提供的变量")
		}
		for _, match := range mailPlaceholder.FindAllStringSubmatch(field, -1) {
			found := false
			for _, v := range mailVariables {
				if v.Name == match[1] {
					found = true
					break
				}
			}
			if !found {
				return t, fmt.Errorf("不支持的模板变量：%s", match[1])
			}
		}
	}
	return t, nil
}
func validateMailTemplates(templates map[string]mailTemplate) error {
	if len(templates) > 5 {
		return errors.New("邮件模板数量无效")
	}
	for kind, t := range templates {
		if !validMailKind(kind) {
			return errors.New("邮件模板类型无效")
		}
		if _, err := normalizeMailTemplate(t); err != nil {
			return err
		}
	}
	return nil
}
func mailItemKind(item mailItem) string {
	if item.recovery {
		return "recovery"
	}
	return item.alert.Kind
}
func mailDeviceNames(snapshot config.State) map[string]string {
	out := map[string]string{}
	for _, d := range snapshot.Devices {
		out[d.ID] = d.Name
	}
	return out
}
func renderMailTemplate(kind string, t mailTemplate, item mailItem, names map[string]string, now time.Time, count int) (string, string) {
	status := "待处理"
	if kind == "recovery" {
		status = "已恢复 / 条件解除"
	}
	if kind == "test" {
		status = "配置测试"
	}
	labels := map[string]string{"offline": "设备离线", "certificate": "证书到期", "update": "更新失败", "recovery": "恢复通知", "test": "测试邮件"}
	domain := "—"
	if strings.HasPrefix(item.alert.Key, "certificate:") {
		endpoint := strings.TrimPrefix(item.alert.Key, "certificate:")
		if host, _, err := net.SplitHostPort(endpoint); err == nil {
			domain = host
		}
	}
	name := names[item.alert.DeviceID]
	if name == "" {
		name = "—"
	}
	deviceID := item.alert.DeviceID
	if deviceID == "" {
		deviceID = "—"
	}
	stamp := func(at time.Time) string {
		if at.IsZero() {
			return "—"
		}
		return at.UTC().Format("2006-01-02 15:04 UTC")
	}
	resolved := "—"
	if item.alert.ResolvedAt != nil {
		resolved = stamp(*item.alert.ResolvedAt)
	}
	values := map[string]string{"device_name": name, "device_id": deviceID, "domain": domain, "title": item.alert.Title, "detail": item.alert.Detail, "status": status, "event_type": labels[kind], "created_at": stamp(item.alert.CreatedAt), "resolved_at": resolved, "time": stamp(now), "count": fmt.Sprint(count)}
	replace := func(text string) string {
		return mailPlaceholder.ReplaceAllStringFunc(text, func(token string) string { return values[mailPlaceholder.FindStringSubmatch(token)[1]] })
	}
	subject := strings.Join(strings.Fields(replace(t.Subject)), " ")
	runes := []rune(subject)
	if len(runes) > 500 {
		subject = string(runes[:500])
	}
	body := strings.ReplaceAll(strings.ReplaceAll(replace(t.Body), "\r\n", "\n"), "\r", "\n")
	return subject, strings.ReplaceAll(body, "\n", "\r\n")
}
func composeConfiguredMail(items []mailItem, templates map[string]mailTemplate, names map[string]string, now time.Time) (string, string) {
	custom := false
	for _, item := range items {
		if _, ok := templates[mailItemKind(item)]; ok {
			custom = true
			break
		}
	}
	if !custom {
		return composeMailDigest(items)
	}
	bodies := []string{}
	subject := ""
	for _, item := range items {
		kind := mailItemKind(item)
		t, ok := templates[kind]
		if !ok {
			t = defaultMailTemplate(kind)
		}
		s, b := renderMailTemplate(kind, t, item, names, now, len(items))
		subject = s
		bodies = append(bodies, b)
	}
	if len(items) > 1 {
		subject = fmt.Sprintf("RemoteGate：%d 项运行通知", len(items))
	}
	return subject, strings.Join(bodies, "\r\n\r\n────────────────\r\n\r\n")
}
func (m *notificationMailer) templateViewLocked() map[string]any {
	effective := map[string]mailTemplate{}
	defaults := map[string]mailTemplate{}
	custom := map[string]bool{}
	for _, kind := range []string{"offline", "certificate", "update", "recovery", "test"} {
		defaults[kind] = defaultMailTemplate(kind)
		t, ok := m.record.Templates[kind]
		if !ok {
			t = defaults[kind]
		}
		effective[kind] = t
		custom[kind] = ok
	}
	return map[string]any{"templates": effective, "defaults": defaults, "custom": custom, "variables": mailVariables}
}
func (a *app) mailTemplatesHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !sameAuthOrigin(r) {
		http.Error(w, "请求来源不匹配", 403)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	m, err := a.mailService()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if r.Method == "POST" {
		var input struct {
			Action   string       `json:"action"`
			Kind     string       `json:"kind"`
			Template mailTemplate `json:"template"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !validMailKind(input.Kind) {
			http.Error(w, "模板参数无效", 400)
			return
		}
		if input.Action != "save" && input.Action != "reset" {
			http.Error(w, "模板操作无效", 400)
			return
		}
		if input.Action == "save" {
			input.Template, err = normalizeMailTemplate(input.Template)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		m.mu.Lock()
		old := m.record.Templates
		next := map[string]mailTemplate{}
		for kind, t := range old {
			next[kind] = t
		}
		if input.Action == "reset" || input.Template == defaultMailTemplate(input.Kind) {
			delete(next, input.Kind)
		} else {
			next[input.Kind] = input.Template
		}
		m.record.Templates = next
		if err = m.saveLocked(); err != nil {
			m.record.Templates = old
			m.mu.Unlock()
			http.Error(w, "模板保存失败，请检查数据目录", 500)
			return
		}
		view := m.templateViewLocked()
		m.mu.Unlock()
		writeJSON(w, view)
		return
	}
	m.mu.Lock()
	view := m.templateViewLocked()
	m.mu.Unlock()
	writeJSON(w, view)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
)

func (a *app) trafficTrends(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.traffic == nil {
		http.Error(w, "流量统计尚未就绪", 503)
		return
	}
	scope, id, period := r.URL.Query().Get("scope"), r.URL.Query().Get("id"), r.URL.Query().Get("period")
	if scope != "devices" && scope != "mappings" || period != "day" && period != "month" {
		http.Error(w, "统计参数无效", 400)
		return
	}
	ids := []string{}
	snapshot := a.store.Snapshot()
	if scope == "devices" {
		for _, d := range snapshot.Devices {
			if id == "" || id == d.ID {
				ids = append(ids, d.ID)
			}
		}
	} else {
		for _, m := range snapshot.Mappings {
			if id == "" || id == m.ID {
				ids = append(ids, m.ID)
			}
		}
	}
	if id != "" && len(ids) == 0 {
		http.Error(w, "设备或映射不存在", 404)
		return
	}
	writeJSON(w, a.traffic.Series(scope, ids, period, time.Now()))
}

func (a *app) notifications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, map[string]any{"items": a.store.Alerts(), "limit": 200})
	case "POST":
		if err := a.store.ReadAlerts(); err != nil {
			http.Error(w, "提醒保存失败，请重试", 500)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *app) maintainAlerts(ctx context.Context) {
	started := time.Now()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := a.store.ReconcileAlerts(a.alertConditions(now, started), now); err != nil {
				log.Printf("异常提醒保存失败：%v", err)
			}
		}
	}
}

func (a *app) alertConditions(now, started time.Time) []config.Alert {
	snapshot, statuses := a.store.Snapshot(), a.hub.Statuses()
	conditions := []config.Alert{}
	existing := map[string]config.Alert{}
	for _, alert := range a.store.Alerts() {
		if alert.ResolvedAt == nil {
			existing[alert.Key] = alert
		}
	}
	for _, d := range snapshot.Devices {
		offlineSince := d.LastDisconnectedAt
		if offlineSince.IsZero() {
			offlineSince = d.CreatedAt
		}
		if offlineSince.Before(started) {
			offlineSince = started
		}
		_, alreadyReported := existing["offline:"+d.ID]
		if _, online := statuses[d.ID]; !online && (now.Sub(offlineSince) >= 3*time.Minute || alreadyReported) {
			conditions = append(conditions, config.Alert{Key: "offline:" + d.ID, Kind: "offline", DeviceID: d.ID, Title: d.Name + "持续离线", Detail: "已连续离线超过 3 分钟，请检查路由器网络及插件连接状态。"})
		}
	}
	for _, d := range a.store.Domains() {
		seen := map[string]bool{}
		checks := append([]config.PublicHTTPSCheck(nil), d.HTTPSChecks...)
		if d.PublicHTTPS != nil {
			checks = append(checks, *d.PublicHTTPS)
		}
		configured := map[string]bool{d.BaseDomain + ":" + strconv.Itoa(effectiveDomainPort(d)): true}
		for _, m := range snapshot.Mappings {
			if m.Enabled && m.PublicScheme != "http" {
				port := m.PublicPort
				if port == 0 {
					port = 443
				}
				configured[m.Host+":"+strconv.Itoa(port)] = true
			}
		}
		for _, c := range checks {
			endpoint := c.Host + ":" + strconv.Itoa(c.Port)
			if seen[endpoint] || !configured[endpoint] {
				continue
			}
			seen[endpoint] = true
			if c.ExpiresAt.IsZero() {
				if prior, ok := existing["certificate:"+endpoint]; ok {
					conditions = append(conditions, prior)
				}
				continue
			}
			if !c.ExpiresAt.IsZero() && c.ExpiresAt.Sub(now) <= 30*24*time.Hour {
				detail := "上次检测到的证书将于 " + c.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC") + " 到期，请在宝塔 / 反向代理确认续期并重新检测。"
				title := endpoint + "证书即将到期"
				if !c.ExpiresAt.After(now) {
					title = endpoint + "证书已过期"
				}
				conditions = append(conditions, config.Alert{Key: "certificate:" + endpoint, Kind: "certificate", Title: title, Detail: detail})
			}
		}
	}
	if p, err := a.packageCenterStore(); err == nil {
		for _, d := range snapshot.Devices {
			state := p.latestUpdateState(d.ID)
			if state.Phase == "failed" {
				detail := "请在安装与更新中心查看结果，再决定是否重试。"
				if state.RolledBack {
					detail = "更新失败，插件已执行回滚；请在安装与更新中心核对恢复结果。"
				}
				conditions = append(conditions, config.Alert{Key: "update:" + d.ID + ":" + state.ID, Kind: "update", DeviceID: d.ID, Title: d.Name + "插件更新失败", Detail: detail})
			}
		}
	}
	if _, err := a.packageCenterStore(); err != nil {
		for _, alert := range a.store.Alerts() {
			if alert.Kind == "update" && alert.ResolvedAt == nil {
				conditions = append(conditions, alert)
			}
		}
	}
	return conditions
}

func effectiveDomainPort(d config.DomainSettings) int {
	if d.RootHTTPSPort == 0 {
		return 443
	}
	return d.RootHTTPSPort
}

type diagnosticStep struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Diagnostics are read-only: never restart a client or change its routing. DNS
// and TLS observe the public edge from the server; the target request goes
// through the existing device tunnel and carries no administrator cookies.
func (a *app) diagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		DeviceID  string `json:"deviceId"`
		MappingID string `json:"mappingId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "参数无效", 400)
		return
	}
	snapshot := a.store.Snapshot()
	var mapping *config.Mapping
	if in.MappingID != "" {
		for _, m := range snapshot.Mappings {
			if m.ID == in.MappingID {
				copy := m
				mapping = &copy
				break
			}
		}
		if mapping == nil {
			http.Error(w, "映射不存在", 404)
			return
		}
		if in.DeviceID != "" && in.DeviceID != mapping.DeviceID {
			http.Error(w, "映射与设备不匹配", 400)
			return
		}
		in.DeviceID = mapping.DeviceID
	}
	exists := false
	for _, d := range snapshot.Devices {
		if d.ID == in.DeviceID {
			exists = true
		}
	}
	if !exists {
		http.Error(w, "设备不存在", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	steps := []diagnosticStep{{"tunnel", "设备隧道", "skipped", ""}, {"dns", "公网 DNS", "skipped", "未选择映射"}, {"tls", "公网 HTTPS", "skipped", "未选择映射"}, {"target", "内网服务", "skipped", "未选择映射"}}
	tunnelDone := make(chan struct{})
	edgeDone := make(chan struct{})
	targetDone := make(chan struct{})
	go func() {
		defer close(tunnelDone)
		if _, ok := a.hub.Statuses()[in.DeviceID]; !ok {
			steps[0].Status = "error"
			steps[0].Detail = "设备当前离线"
			return
		}
		pingCtx, c := context.WithTimeout(ctx, 8*time.Second)
		defer c()
		_, err := a.hub.Ping(pingCtx, in.DeviceID)
		if err != nil {
			steps[0].Status = "warn"
			steps[0].Detail = "隧道存在，但响应超时；未修改设备在线状态"
		} else {
			steps[0].Status = "ok"
			steps[0].Detail = "设备已响应隧道检测"
		}
	}()
	go func() {
		defer close(edgeDone)
		if mapping == nil {
			return
		}
		port := mapping.PublicPort
		if port == 0 {
			port = 443
			if mapping.PublicScheme == "http" {
				port = 80
			}
		}
		d := config.DomainSettings{BaseDomain: mapping.Host}
		for _, candidate := range a.store.Domains() {
			if mapping.Host == candidate.BaseDomain || stringsHasDomain(mapping.Host, candidate.BaseDomain) {
				if len(candidate.BaseDomain) > len(d.BaseDomain) || d.ID == "" {
					d = candidate
				}
			}
		}
		edgeCtx, c := context.WithTimeout(ctx, 10*time.Second)
		defer c()
		rows, checks, _ := checkDomainEndpoints(edgeCtx, d, []domainCheckTarget{{Host: mapping.Host, Port: port, HTTPS: mapping.PublicScheme != "http"}})
		row := rows[0]
		steps[1].Status = "ok"
		steps[1].Detail = "域名已解析；未配置参考服务器 IP"
		if _, ok := row["error"]; ok {
			steps[1].Status = "error"
			steps[1].Detail = "域名未解析或 DNS 查询失败"
			steps[2].Detail = "DNS 失败，未能检测证书"
			return
		}
		if d.ServerIP != "" {
			if row["matches"] == true {
				steps[1].Detail = "解析地址包含配置的服务器 IP"
			} else {
				steps[1].Status = "warn"
				steps[1].Detail = "解析 IP 与配置不同；若使用 CDN，请核对代理设置"
			}
		}
		if mapping.PublicScheme == "http" {
			steps[2].Detail = "此映射使用 HTTP，无 HTTPS 证书"
			return
		}
		steps[2].Status = "error"
		steps[2].Detail = "公网 HTTPS 检测失败，请在域名与 HTTPS 查看详情"
		if certificate, ok := row["certificate"].(map[string]any); ok && certificate["valid"] == true {
			steps[2].Status = "ok"
			steps[2].Detail = "信任链、域名与有效期验证通过"
		}
		if d.ID != "" {
			if err := a.store.SetDomainHTTPSChecks(d.ID, checks); err != nil {
				steps[2].Detail += "；检测结果未能保存"
			}
		}
	}()
	go func() {
		defer close(targetDone)
		if mapping == nil {
			return
		}
		if _, ok := a.hub.Statuses()[in.DeviceID]; !ok {
			steps[3].Detail = "设备离线，无法检测内网服务"
			return
		}
		targetCtx, c := context.WithTimeout(ctx, 10*time.Second)
		defer c()
		response, err := a.hub.Request(targetCtx, in.DeviceID, protocol.Message{Type: "request", ID: makeID(), Method: "HEAD", Target: mapping.Target, Path: "/"}, mapping.ID)
		steps[3].Status = "error"
		steps[3].Detail = "未收到内网 HTTP 响应，请检查服务地址与监听端口"
		if err == nil && response.Error == "" && response.Status >= 100 {
			steps[3].Status = "ok"
			if response.Status >= 400 {
				steps[3].Status = "warn"
			}
			steps[3].Detail = fmt.Sprintf("内网服务返回 HTTP %d", response.Status)
			switch response.Status {
			case 401, 403:
				steps[3].Detail += "，需要使用内网服务自己的账号登录"
			case 405:
				steps[3].Detail += "，此服务可能不支持 HEAD 检测，请打开访问地址确认"
			default:
				if response.Status >= 500 {
					steps[3].Detail += "，服务已响应但返回错误，请查看目标服务日志"
				}
			}
		}
	}()
	<-tunnelDone
	<-edgeDone
	<-targetDone
	if r.Context().Err() != nil {
		return
	}
	writeJSON(w, map[string]any{"steps": steps, "checkedAt": time.Now().UTC(), "mappingEnabled": mapping == nil || mapping.Enabled})
}

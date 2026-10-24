package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/traffic"
)

func TestAutomaticCertificateQueueIncludesEveryConfiguredEndpointAndBacksOff(t *testing.T) {
	now := time.Now()
	d := config.DomainSettings{ID: "test", BaseDomain: "example.com", RootHTTPSPort: 8443}
	maps := []config.Mapping{}
	for i := 0; i < 60; i++ {
		maps = append(maps, config.Mapping{Host: fmt.Sprintf("r%d.example.com", i), Enabled: true, PublicPort: 443})
	}
	maps = append(maps, config.Mapping{Host: "off.example.com"}, config.Mapping{Host: "http.example.com", Enabled: true, PublicScheme: "http"}, config.Mapping{Host: "other.test", Enabled: true})
	tasks := certificateTasks([]config.DomainSettings{d}, maps, now)
	if len(tasks) != 61 || tasks[0].target.Host != d.BaseDomain || tasks[0].target.Port != 8443 {
		t.Fatal("endpoints omitted", len(tasks))
	}
	d.PublicHTTPS = &config.PublicHTTPSCheck{Host: d.BaseDomain, Port: 8443, CheckedAt: now.Add(-25 * time.Hour), Valid: true}
	d.HTTPSChecks = []config.PublicHTTPSCheck{{Host: maps[0].Host, Port: 443, CheckedAt: now.Add(-time.Minute), Valid: false}, {Host: maps[1].Host, Port: 443, CheckedAt: now.Add(-2 * time.Hour), Valid: false}, {Host: maps[2].Host, Port: 443, CheckedAt: now.Add(-2 * time.Hour), Valid: true}}
	tasks = certificateTasks([]config.DomainSettings{d}, maps, now)
	if len(tasks) != 59 {
		t.Fatal("backoff ignored", len(tasks))
	}
	for _, task := range tasks {
		if task.target.Host == maps[0].Host || task.target.Host == maps[2].Host {
			t.Fatal("fresh entry was rechecked")
		}
	}
	if tasks[len(tasks)-1].target.Host != maps[1].Host {
		t.Fatal("oldest observations not prioritized")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { defer close(done); (&app{}).maintainCertificates(ctx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor ignored shutdown")
	}
}

func TestOperationsAPIsRequireLoginOriginAndKnownEntities(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	d, _, _ := a.store.CreateDevice("router")
	a.traffic = traffic.New()
	a.traffic.Add(d.ID, "", 12, 34)
	for _, path := range []string{"/api/admin/notifications", "/api/admin/traffic/trends?scope=devices&period=day", "/api/admin/diagnose"} {
		requireStatus(t, authCall(a, path, "", "", nil), 401)
	}
	requireStatus(t, authCall(a, "/api/admin/diagnose", "{}", "https://evil.example", cookie), 403)
	requireStatus(t, authCall(a, "/api/admin/notifications", "{}", "https://evil.example", cookie), 403)
	requireStatus(t, authCall(a, "/api/admin/diagnose", "{}", "", cookie), 404)
	requireStatus(t, authCall(a, "/api/admin/traffic/trends?scope=anything&period=day", "", "", cookie), 400)
	requireStatus(t, authCall(a, "/api/admin/traffic/trends?scope=devices&period=day&id=missing", "", "", cookie), 404)
	result := authCall(a, "/api/admin/traffic/trends?scope=devices&period=day", "", "", cookie)
	requireStatus(t, result, 200)
	if !strings.Contains(result.Body.String(), `"uploadBytes":12`) || strings.Contains(result.Body.String(), "tokenHash") {
		t.Fatal("trend API attribution or privacy error")
	}
	diagnostic := authCall(a, "/api/admin/diagnose", `{"deviceId":"`+d.ID+`"}`, "", cookie)
	requireStatus(t, diagnostic, 200)
	var data struct {
		Steps []diagnosticStep `json:"steps"`
	}
	json.Unmarshal(diagnostic.Body.Bytes(), &data)
	if len(data.Steps) != 4 || data.Steps[0].Status != "error" || data.Steps[3].Status != "skipped" {
		t.Fatal(data)
	}
	if len(a.store.Alerts()) != 0 {
		t.Fatal("diagnosis altered alert history")
	}
	a.store.ReconcileAlerts([]config.Alert{{Key: "test", Title: "test"}}, time.Now())
	result = authCall(a, "/api/admin/notifications", "", "", cookie)
	requireStatus(t, result, 200)
	if strings.Contains(result.Body.String(), `"resolvedAt"`) {
		t.Fatal("active alert incorrectly serialized as resolved")
	}
	requireStatus(t, authCall(a, "/api/admin/notifications", "{}", "", cookie), 204)
	if !a.store.Alerts()[0].Read {
		t.Fatal("mark read not saved")
	}
}

func TestAlertDebounceCertificateRenewalAndDeletedEndpoints(t *testing.T) {
	a := freshAuthApp(t)
	now := time.Now().UTC()
	d, _, _ := a.store.CreateDevice("router")
	domain, err := a.store.PutDomain(config.DomainSettings{BaseDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	a.store.SetDomainHTTPSChecks(domain.ID, []config.PublicHTTPSCheck{{Host: domain.BaseDomain, Port: 443, Valid: true, CheckedAt: now, ExpiresAt: now.Add(20 * 24 * time.Hour)}, {Host: "removed.example.com", Port: 443, Valid: true, CheckedAt: now, ExpiresAt: now.Add(time.Hour)}})
	conditions := a.alertConditions(now, now)
	if len(conditions) != 1 || conditions[0].Kind != "certificate" {
		t.Fatal("startup offline alert or deleted endpoint alert", conditions)
	}
	conditions = a.alertConditions(now.Add(4*time.Minute), now)
	if len(conditions) != 2 {
		t.Fatal("persistent offline not detected", conditions)
	}
	found := false
	for _, c := range conditions {
		if c.DeviceID == d.ID && c.Kind == "offline" {
			found = true
		}
	}
	if !found {
		t.Fatal("wrong offline device")
	}
	a.store.ReconcileAlerts(conditions, now)
	if conditions := a.alertConditions(now.Add(4*time.Minute), now.Add(4*time.Minute)); len(conditions) != 2 {
		t.Fatal("restart falsely resolved existing offline alert", conditions)
	}
	a.store.SetDomainHTTPSChecks(domain.ID, []config.PublicHTTPSCheck{{Host: domain.BaseDomain, Port: 443, CheckedAt: now, Error: "DNS timeout"}})
	if conditions := a.alertConditions(now.Add(4*time.Minute), now); len(conditions) != 2 {
		t.Fatal("network failure falsely resolved certificate warning", conditions)
	}
	a.store.SetDomainHTTPSChecks(domain.ID, []config.PublicHTTPSCheck{{Host: domain.BaseDomain, Port: 443, Valid: true, CheckedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)}})
	a.store.ReconcileAlerts(a.alertConditions(now.Add(4*time.Minute), now), now)
	for _, alert := range a.store.Alerts() {
		if alert.Kind == "certificate" && alert.ResolvedAt == nil {
			t.Fatal("renewal failed to resolve warning")
		}
	}
}

func TestDiagnosticRejectsDeviceMappingMismatchAndUsesNoCredentials(t *testing.T) {
	a := freshAuthApp(t)
	one, _, _ := a.store.CreateDevice("one")
	two, _, _ := a.store.CreateDevice("two")
	mapping, err := a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: one.ID, Target: "http://127.0.0.1:80", AccessMode: "public", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/admin/diagnose", strings.NewReader(`{"deviceId":"`+two.ID+`","mappingId":"`+mapping.ID+`"}`))
	w := httptest.NewRecorder()
	a.diagnose(w, req)
	requireStatus(t, w, 400)
	if strings.Contains(w.Body.String(), "token") {
		t.Fatal("diagnostic leaked identity")
	}
}

func TestDiagnosticOnlineTunnelAndTargetAreIndependentOfDNSFailure(t *testing.T) {
	a := freshAuthApp(t)
	device, token, _ := a.store.CreateDevice("router")
	mapping, err := a.store.PutMapping(config.Mapping{Host: "diagnosis.invalid", PublicScheme: "http", DeviceID: device.ID, Target: "http://127.0.0.1:80", AccessMode: "public", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := agentTestServer(t, a)
	defer server.Close()
	query := url.Values{"device_id": {device.ID}, "token": {token}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requests := make(chan protocol.Message, 1)
	go func() {
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			requests <- msg
			if conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 403}) != nil {
				return
			}
		}
	}()
	waitDevice(t, a, device.ID, true)
	req := httptest.NewRequest("POST", "/api/admin/diagnose", strings.NewReader(`{"mappingId":"`+mapping.ID+`"}`))
	req.Header.Set("Cookie", "secret-admin-cookie")
	w := httptest.NewRecorder()
	a.diagnose(w, req)
	requireStatus(t, w, 200)
	var result struct {
		Steps []diagnosticStep `json:"steps"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Steps[0].Status != "ok" || result.Steps[3].Status != "warn" || !strings.Contains(result.Steps[3].Detail, "403") {
		t.Fatal(result)
	}
	select {
	case msg := <-requests:
		if msg.Method != "HEAD" || msg.Target != mapping.Target || len(msg.Headers) != 0 {
			t.Fatal("diagnosis forwarded credentials or wrong request")
		}
	default:
		t.Fatal("target was never checked")
	}
	if _, online := a.hub.Statuses()[device.ID]; !online {
		t.Fatal("diagnosis disconnected device")
	}
}

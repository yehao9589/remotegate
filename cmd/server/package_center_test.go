package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

func packageCall(a *app, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://console.localhost"+path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "http://console.localhost")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func writeCenterPackage(t *testing.T, dir, version string) (string, update.Manifest) {
	t.Helper()
	var content bytes.Buffer
	content.WriteString("#!/bin/sh\nexit 0\n__PAYLOAD__\n")
	z := gzip.NewWriter(&content)
	tw := tar.NewWriter(z)
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		payload := make([]byte, 20000)
		_, _ = rand.Read(payload)
		_ = tw.WriteHeader(&tar.Header{Name: "remotegate-" + arch + ".ipk", Size: int64(len(payload)), Mode: 0644, Typeflag: tar.TypeReg})
		_, _ = tw.Write(payload)
	}
	_ = tw.Close()
	_ = z.Close()
	hash := sha256.Sum256(content.Bytes())
	m := update.Manifest{PackageName: "luci-app-remotegate", PackageVersion: version, AgentVersion: strings.Split(version, "-")[0], SHA256: hex.EncodeToString(hash[:]), Size: int64(content.Len()), Architectures: []string{"x86_64", "ARM64", "ARMv7"}}
	path := filepath.Join(dir, "RemoteGate-"+version+"-istore.run")
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(path+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, m
}
func centerFixture(t *testing.T) (*app, config.Device, string, *http.Cookie) {
	t.Helper()
	a, device, token, cookie, _ := pluginFixture(t)
	path, _ := writeCenterPackage(t, t.TempDir(), buildinfo.PackageVersion())
	t.Setenv("INSTALLER_PATH", path)
	return a, device, token, cookie
}
func TestPackageProfilesPolicyHistoryPersistenceAndAuthentication(t *testing.T) {
	a, device, token, cookie := centerFixture(t)
	body := installProfile{Name: "家庭路由器", Server: "https://console.example.com", RemoteAccess: false, AutoStart: false, InterfaceMode: "select"}
	requireStatus(t, packageCall(a, nil, "PUT", "/api/admin/package-center/profile", body), 401)
	saved := packageCall(a, cookie, "PUT", "/api/admin/package-center/profile", body)
	requireStatus(t, saved, 200)
	var profile installProfile
	_ = json.Unmarshal(saved.Body.Bytes(), &profile)
	if profile.ID == "" || profile.RemoteAccess || profile.AutoStart {
		t.Fatal(profile)
	}
	body.Server = "https://user:secret@console.example.com"
	requireStatus(t, packageCall(a, cookie, "PUT", "/api/admin/package-center/profile", body), 400)
	requireStatus(t, packageCall(a, cookie, "PUT", "/api/admin/package-center/policy", map[string]any{"deviceId": device.ID, "locked": true}), 200)
	req := httptest.NewRequest("GET", "/api/agent/update", nil)
	req.Header.Set("X-RemoteGate-Device-ID", device.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, req)
	requireStatus(t, w, 409)
	p, _ := a.packageCenterStore()
	id, err := p.record(device.ID, device.Name, "update", update.Status{Phase: "failed", Message: "grant=" + strings.Repeat("a", 64), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	a.centerMu.Lock()
	a.center = nil
	a.centerMu.Unlock()
	reloaded, _ := a.packageCenterStore()
	if !reloaded.locked(device.ID) {
		t.Fatal("lock did not survive restart")
	}
	profiles := reloaded.profileSnapshot()
	if len(profiles) != 1 || profiles[0].RemoteAccess || profiles[0].AutoStart {
		t.Fatal("profile did not survive restart")
	}
	report := packageCall(a, cookie, "GET", "/api/admin/package-center/report?id="+id, nil)
	requireStatus(t, report, 200)
	if strings.Contains(report.Body.String(), strings.Repeat("a", 64)) || !strings.Contains(report.Body.String(), "[hidden]") {
		t.Fatal("grant leaked into report", report.Body.String())
	}
	requireStatus(t, packageCall(a, cookie, "POST", "/api/admin/package-center/profile/remove", map[string]string{"id": profile.ID}), 204)
	if len(reloaded.profileSnapshot()) != 0 {
		t.Fatal("profile was not removed")
	}
}
func TestPackageArchitectureDownloadAndFrozenDeviceGrant(t *testing.T) {
	a, device, token, cookie := centerFixture(t)
	p, _ := a.packageCenterStore()
	base, err := a.packageArtifact(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := a.packageArtifact(p, "", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Manifest.Size >= base.Manifest.Size || len(selected.Manifest.Architectures) != 1 {
		t.Fatal("package was not reduced")
	}
	frozen, err := a.issuePackageGrant(p, device.ID, selected, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/agent/update/package?artifact=" + frozen.Artifact + "&grant=" + frozen.Grant
	call := func(id, secret string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-RemoteGate-Device-ID", id)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		return w
	}
	w := call(device.ID, token)
	requireStatus(t, w, 200)
	hash := sha256.Sum256(w.Body.Bytes())
	if hex.EncodeToString(hash[:]) != selected.Manifest.SHA256 {
		t.Fatal("download changed")
	}
	other, otherToken, _ := a.store.CreateDevice("other")
	requireStatus(t, call(other.ID, otherToken), 403)
	metadata := packageCall(a, cookie, "GET", "/api/admin/package-center", nil)
	requireStatus(t, metadata, 200)
	if strings.Contains(metadata.Body.String(), frozen.Grant) {
		t.Fatal("grant leaked into package center")
	}
	if err := os.WriteFile(selected.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(device.ID, token), 403)
}
func TestInstallTemplateProgressIsScopedOrderedAndNotACompletionClaim(t *testing.T) {
	a, device, token, cookie := centerFixture(t)
	profile := installProfile{Name: "不随开机启动", RemoteAccess: false, AutoStart: false, InterfaceMode: "select"}
	saved := packageCall(a, cookie, "PUT", "/api/admin/package-center/profile", profile)
	requireStatus(t, saved, 200)
	_ = json.Unmarshal(saved.Body.Bytes(), &profile)
	generated := packageCall(a, cookie, "POST", "/api/admin/install", map[string]string{"deviceId": device.ID, "token": token, "server": "http://10.0.2.2:18088", "profileId": profile.ID})
	requireStatus(t, generated, 200)
	var command struct {
		Command     string `json:"command"`
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(generated.Body.Bytes(), &command)
	keyURL := strings.Split(command.Command, "'")[1]
	key := strings.TrimPrefix(strings.Split(keyURL, "/install/")[1], "/")
	installs.Lock()
	ticket := installs.items[key]
	installs.Unlock()
	script := httptest.NewRecorder()
	a.installScript(script, httptest.NewRequest("GET", keyURL, nil))
	requireStatus(t, script, 200)
	for _, snippet := range []string{"remote_access=0", "remotegate disable", "/sys/class/net/*", "configuration.backup", "?artifact="} {
		if !strings.Contains(script.Body.String(), snippet) {
			t.Fatal("missing template behavior", snippet)
		}
	}
	report := func(secret, phase string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/install/report", strings.NewReader(`{"phase":"`+phase+`"}`))
		r.Header.Set("X-RemoteGate-Install-Key", secret)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		return w
	}
	requireStatus(t, report("wrong", "preflight"), 403)
	requireStatus(t, report(ticket.ReportKey, "preflight"), 204)
	requireStatus(t, report(ticket.ReportKey, "downloading"), 204)
	requireStatus(t, report(ticket.ReportKey, "preflight"), 409)
	requireStatus(t, report(ticket.ReportKey, "succeeded"), 400)
	p, _ := a.packageCenterStore()
	p.mu.Lock()
	last := p.state.History[len(p.state.History)-1]
	p.mu.Unlock()
	if last.ID != command.OperationID || last.State.Phase != "downloading" {
		t.Fatal(last)
	}
}
func connectCenterDevice(t *testing.T, a *app, device config.Device, token, version string) *websocket.Conn {
	t.Helper()
	server := agentTestServer(t, a)
	query := url.Values{"device_id": {device.ID}, "token": {token}, "version": {strings.Split(version, "-")[0]}, "package_version": {version}, "updater": {"1"}, "update_tools": {"1"}, "info": {"1"}, "os": {"linux"}, "arch": {"amd64"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); a.hub.Disconnect(device.ID) })
	waitDevice(t, a, device.ID, true)
	return conn
}
func TestCompatibleRollbackRequiresCapabilityAndScopedApproval(t *testing.T) {
	a, device, token, cookie := centerFixture(t)
	_, old := writeCenterPackage(t, filepath.Dir(installerPath()), buildinfo.Version()+"-1")
	conn := connectCenterDevice(t, a, device, token, buildinfo.PackageVersion())
	finished := make(chan update.Manifest, 1)
	go func() {
		var msg protocol.Message
		if conn.ReadJSON(&msg) != nil {
			return
		}
		var m update.Manifest
		_ = json.Unmarshal(msg.Body, &m)
		finished <- m
		body, _ := json.Marshal(pluginUpdateView{State: update.Status{ID: "rollback-op", Phase: "checking", FromVersion: buildinfo.PackageVersion(), ToVersion: old.PackageVersion, StartedAt: time.Now()}})
		_ = conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: body})
	}()
	r := packageCall(a, cookie, "POST", "/api/admin/package-center/rollback", map[string]string{"deviceId": device.ID, "version": old.PackageVersion})
	requireStatus(t, r, 200)
	select {
	case m := <-finished:
		if !m.Rollback || m.Grant == "" || m.Artifact == "" || m.AgentVersion != buildinfo.Version() {
			t.Fatal(m)
		}
	case <-time.After(time.Second):
		t.Fatal("no rollback approval sent")
	}
	if strings.Contains(r.Body.String(), `"grant"`) {
		t.Fatal("lease was exposed to the browser")
	}
	requireStatus(t, packageCall(a, cookie, "POST", "/api/admin/package-center/rollback", map[string]string{"deviceId": device.ID, "version": "0.1.9-1"}), 409)
}
func TestBatchUpdatePausesAfterFailureWithoutTouchingNextDevice(t *testing.T) {
	a, first, token, cookie := centerFixture(t)
	conn := connectUpdateDevice(t, a, first, token, true)
	second, secondToken, _ := a.store.CreateDevice("second")
	_ = a.store.ActivateWithPackage(second.ID, secondToken, "0.1.3", "0.1.3-1")
	otherConn := connectUpdateDevice(t, a, second, secondToken, true)
	go func() {
		var msg protocol.Message
		if conn.ReadJSON(&msg) == nil {
			_ = conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Error: "fixture install failed"})
		}
	}()
	response := packageCall(a, cookie, "POST", "/api/admin/package-center/batch", map[string]any{"deviceIds": []string{first.ID, second.ID}, "stopOnFailure": true})
	requireStatus(t, response, 200)
	p, _ := a.packageCenterStore()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		paused := p.state.Batch.Phase == "paused"
		items := append([]packageBatchItem(nil), p.state.Batch.Items...)
		worker := p.batchWorker
		p.mu.Unlock()
		if paused && !worker {
			if items[0].Phase != "failed" || items[1].Phase != "pending" {
				t.Fatal(items)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.mu.Lock()
	paused := p.state.Batch.Phase == "paused"
	p.mu.Unlock()
	if !paused {
		t.Fatal("batch did not pause")
	}
	_ = otherConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var msg protocol.Message
	if otherConn.ReadJSON(&msg) == nil {
		t.Fatal("next device received an update after the first failed")
	}
}

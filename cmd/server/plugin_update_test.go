package main

import (
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

func pluginFixture(t *testing.T) (*app, config.Device, string, *http.Cookie, update.Manifest) {
	t.Helper()
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	device, token, err := a.store.CreateDevice("router")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.ActivateWithPackage(device.ID, token, "0.1.3", "0.1.3-1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "installer.run")
	data := []byte("test installer")
	hash := sha256.Sum256(data)
	manifest := update.Manifest{PackageName: "luci-app-remotegate", PackageVersion: buildinfo.PackageVersion(), AgentVersion: buildinfo.Version(), SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), Architectures: []string{"x86_64", "ARM64", "ARMv7"}}
	raw, _ := json.Marshal(manifest)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INSTALLER_PATH", path)
	return a, device, token, cookie, manifest
}

func connectUpdateDevice(t *testing.T, a *app, device config.Device, token string, supported bool, packageVersions ...string) *websocket.Conn {
	t.Helper()
	server := agentTestServer(t, a)
	query := url.Values{"device_id": {device.ID}, "token": {token}, "version": {"0.1.3"}, "package_version": {"0.1.3-1"}}
	if supported {
		query.Set("updater", "1")
	}
	if len(packageVersions) > 0 {
		query.Set("package_version", packageVersions[0])
	}
	if len(packageVersions) > 1 && packageVersions[1] == "recovery" {
		query.Set("update_recovery", "1")
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); a.hub.Disconnect(device.ID) })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, online := a.hub.Statuses()[device.ID]; online {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return conn
}

func TestPluginResourcesRequireDeviceCredentialsAndVerifiedPackage(t *testing.T) {
	a, device, token, _, manifest := pluginFixture(t)
	for _, resource := range []string{"/api/agent/update", "/api/agent/update/package"} {
		for _, secret := range []string{"", "wrong", token} {
			req := httptest.NewRequest("GET", resource, nil)
			req.Header.Set("X-RemoteGate-Device-ID", device.ID)
			req.Header.Set("Authorization", "Bearer "+secret)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, req)
			want := 401
			if secret == token {
				want = 200
			}
			requireStatus(t, w, want)
			if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("update response cached")
			}
		}
	}
	if manifest.Size == 0 {
		t.Fatal("size missing")
	}
	os.WriteFile(installerPath(), []byte("tampered"), 0600)
	if _, err := a.loadUpdatePackage(); err == nil {
		t.Fatal("tampered installer accepted")
	}
}

func TestRemoteUpdateAuthorizationAndLegacyCapability(t *testing.T) {
	a, device, token, cookie, manifest := pluginFixture(t)
	connectUpdateDevice(t, a, device, token, false)
	path := "/api/admin/devices/" + device.ID + "/update"
	body, _ := json.Marshal(manifest)
	requireStatus(t, authCall(a, path, string(body), "", nil), 401)
	requireStatus(t, authCall(a, path, string(body), "https://evil.example", cookie), 403)
	start := time.Now()
	requireStatus(t, authCall(a, path, string(body), "", cookie), 409)
	if time.Since(start) > time.Second {
		t.Fatal("sent unsupported update command to legacy agent")
	}
	response := authCall(a, path, "", "", cookie)
	requireStatus(t, response, 200)
	var view pluginUpdateView
	json.Unmarshal(response.Body.Bytes(), &view)
	if !view.Online || view.CanUpdate || !view.Available {
		t.Fatal(view)
	}
}

func TestRemoteUpdateUsesFrozenPackageAndReportsReconnection(t *testing.T) {
	a, device, token, cookie, manifest := pluginFixture(t)
	conn := connectUpdateDevice(t, a, device, token, true)
	path := "/api/admin/devices/" + device.ID + "/update"
	wrong := manifest
	wrong.SHA256 = strings.Repeat("0", 64)
	body, _ := json.Marshal(wrong)
	requireStatus(t, authCall(a, path, string(body), "", cookie), 409)
	finished := make(chan error, 1)
	go func() {
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			finished <- err
			return
		}
		if msg.Type != "update-start" || msg.Deadline <= time.Now().UnixMilli() {
			finished <- os.ErrInvalid
			return
		}
		var received update.Manifest
		if json.Unmarshal(msg.Body, &received) != nil || received.SHA256 != manifest.SHA256 {
			finished <- os.ErrInvalid
			return
		}
		raw, _ := json.Marshal(pluginUpdateView{InstalledVersion: "0.1.3-1", State: update.Status{ID: "operation", Phase: "downloading", StartedAt: time.Now(), ToVersion: manifest.PackageVersion}})
		finished <- conn.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: raw})
	}()
	body, _ = json.Marshal(manifest)
	response := authCall(a, path, string(body), "", cookie)
	requireStatus(t, response, 200)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var view pluginUpdateView
	json.Unmarshal(response.Body.Bytes(), &view)
	if !view.State.Active() {
		t.Fatal(view)
	}
	requireStatus(t, authCall(a, path, string(body), "", cookie), 409)
	a.hub.Disconnect(device.ID)
	conn.Close()
	offline := authCall(a, path, "", "", cookie)
	requireStatus(t, offline, 200)
	if !strings.Contains(offline.Body.String(), "downloading") {
		t.Fatal("lost progress while restarting")
	}
	if err := a.store.ActivateWithPackage(device.ID, token, manifest.AgentVersion, manifest.PackageVersion); err != nil {
		t.Fatal(err)
	}
	// The replacement connection confirms the version; no status claim alone proves reconnect.
	reconnected := connectUpdateDevice(t, a, device, token, true, manifest.PackageVersion)
	view, _ = a.pluginUpdateView(device.ID)
	if view.State.Phase != "succeeded" || view.Available {
		t.Fatal(view)
	}
	a.updateMu.Lock()
	staleActive := a.deviceUpdates[device.ID].State.Active()
	a.updateMu.Unlock()
	if staleActive {
		t.Fatal("confirmed reconnect left the updater slot active")
	}
	go func() {
		var msg protocol.Message
		if reconnected.ReadJSON(&msg) != nil {
			return
		}
		raw, _ := json.Marshal(pluginUpdateView{InstalledVersion: manifest.PackageVersion, State: update.Status{Phase: "idle"}})
		_ = reconnected.WriteJSON(protocol.Message{Type: "response", ID: msg.ID, Status: 200, Body: raw})
	}()
	response = authCall(a, path, "", "", cookie)
	requireStatus(t, response, 200)
	json.Unmarshal(response.Body.Bytes(), &view)
	if view.State.Phase != "succeeded" {
		t.Fatal("reboot lost the confirmed completion", view)
	}
}

func TestRecoveryUpdateDoesNotCompleteOnRegistration(t *testing.T) {
	a, device, token, _, manifest := pluginFixture(t)
	if err := a.store.ActivateWithPackage(device.ID, token, manifest.AgentVersion, manifest.PackageVersion); err != nil {
		t.Fatal(err)
	}
	connectUpdateDevice(t, a, device, token, true, manifest.PackageVersion, "recovery")
	state := update.Status{ID: "recovery-operation", Phase: "verifying", AutoRollback: true, StartedAt: time.Now().Add(-time.Minute), FromVersion: "0.1.3-1", ToVersion: manifest.PackageVersion}
	// Even when the initial worker acknowledgement was lost, registration
	// advertises that the worker must finish its own health verification.
	state.AutoRollback = false
	a.rememberPluginUpdate(device.ID, pluginUpdateView{State: state})
	p, err := a.packageCenterStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.record(device.ID, device.Name, "update", state); err != nil {
		t.Fatal(err)
	}
	view, _ := a.pluginUpdateView(device.ID)
	if view.State.Phase != "verifying" || !view.State.Active() {
		t.Fatal("registration skipped health verification", view)
	}
	a.reconcilePackageOperations(p)
	if p.latestUpdateState(device.ID).Phase != "verifying" {
		t.Fatal("history prematurely completed protected update")
	}
	state.Phase, state.Message, state.RolledBack, state.RollbackVerified = "failed", "已自动恢复旧版", true, true
	a.rememberPluginUpdate(device.ID, pluginUpdateView{State: state})
	a.reconcilePackageOperations(p)
	final := p.latestUpdateState(device.ID)
	if final.Phase != "failed" || !final.RolledBack || !final.RollbackVerified {
		t.Fatal("rollback result lost", final)
	}
}

func TestBootstrapUpgradeIsSingleUseAndPreservesExistingDevice(t *testing.T) {
	a, device, token, cookie, _ := pluginFixture(t)
	_, err := a.store.PutMapping(config.Mapping{Host: "router.example.com", DeviceID: device.ID, Target: "http://127.0.0.1:80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	before := a.store.Snapshot()
	response := authCall(a, "/api/admin/devices/"+device.ID+"/update/bootstrap", `{"server":"https://console.example.com"}`, "", cookie)
	requireStatus(t, response, 200)
	var command struct {
		Command string `json:"command"`
	}
	json.Unmarshal(response.Body.Bytes(), &command)
	if strings.Contains(command.Command, token) {
		t.Fatal("device token exposed in command")
	}
	link := strings.Split(command.Command, "'")[1]
	for i, want := range []int{200, 410} {
		w := httptest.NewRecorder()
		a.bootstrapUpdateDownload(w, httptest.NewRequest("GET", link, nil))
		requireStatus(t, w, want)
		if i == 0 {
			script := w.Body.String()
			for _, required := range []string{"configuration.backup", "sha256sum -c", "--update", "trap cleanup EXIT"} {
				if !strings.Contains(script, required) {
					t.Fatal(required)
				}
			}
			if strings.Contains(script, "uci set") || strings.Contains(script, token) {
				t.Fatal("bootstrap resets device")
			}
		}
	}
	for _, want := range []int{200, 410} {
		w := httptest.NewRecorder()
		a.bootstrapUpdateDownload(w, httptest.NewRequest("GET", link+"/package", nil))
		requireStatus(t, w, want)
	}
	after := a.store.Snapshot()
	if len(after.Devices) != len(before.Devices) || len(after.Mappings) != len(before.Mappings) || after.Devices[0].ID != device.ID {
		t.Fatal("bootstrap altered mappings or device")
	}
}

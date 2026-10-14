package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/update"
)

type installProfile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Server        string `json:"server"`
	RemoteAccess  bool   `json:"remoteAccess"`
	AutoStart     bool   `json:"autoStart"`
	InterfaceMode string `json:"interfaceMode"`
}
type versionPolicy struct {
	Locked    bool      `json:"locked"`
	Version   string    `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type packageEvent struct {
	Phase   string    `json:"phase"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}
type packageOperation struct {
	ID         string         `json:"id"`
	DeviceID   string         `json:"deviceId"`
	DeviceName string         `json:"deviceName"`
	Kind       string         `json:"kind"`
	State      update.Status  `json:"state"`
	Events     []packageEvent `json:"events"`
}
type packageBatchItem struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
}
type packageBatch struct {
	ID            string             `json:"id"`
	Phase         string             `json:"phase"`
	Version       string             `json:"version"`
	SHA256        string             `json:"sha256"`
	StopOnFailure bool               `json:"stopOnFailure"`
	Items         []packageBatchItem `json:"items"`
	StartedAt     time.Time          `json:"startedAt"`
}
type packageCenterState struct {
	Profiles []installProfile         `json:"profiles"`
	Policies map[string]versionPolicy `json:"policies"`
	History  []packageOperation       `json:"history"`
	Batch    *packageBatch            `json:"batch,omitempty"`
}
type packageArtifact struct {
	Manifest update.Manifest `json:"manifest"`
	Path     string          `json:"-"`
	Arch     string          `json:"arch"`
	Current  bool            `json:"current"`
}
type packageGrant struct {
	Device   string
	Artifact packageArtifact
	Rollback bool
	Expires  time.Time
}
type installProgressGrant struct {
	Operation, Device string
	Expires           time.Time
}
type packageCenter struct {
	mu          sync.Mutex
	path        string
	directory   string
	state       packageCenterState
	artifacts   map[string]packageArtifact
	grants      map[string]packageGrant
	installs    map[string]installProgressGrant
	batchWorker bool
	catalogHash string
}

func standardProfile() installProfile {
	return installProfile{ID: "standard", Name: "标准接入", RemoteAccess: true, AutoStart: true, InterfaceMode: "auto"}
}
func packageSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
func (a *app) packageCenterStore() (*packageCenter, error) {
	a.centerMu.Lock()
	defer a.centerMu.Unlock()
	if a.center != nil || a.centerErr != nil {
		return a.center, a.centerErr
	}
	p := &packageCenter{path: filepath.Join(a.store.Directory(), "package-center.json"), directory: filepath.Join(a.store.Directory(), "packages"), artifacts: make(map[string]packageArtifact), grants: make(map[string]packageGrant), installs: make(map[string]installProgressGrant)}
	raw, err := os.ReadFile(p.path)
	if err != nil && !os.IsNotExist(err) {
		a.centerErr = err
		return nil, err
	}
	if len(raw) > 2<<20 || (len(raw) > 0 && json.Unmarshal(raw, &p.state) != nil) {
		a.centerErr = errors.New("安装与更新记录无法读取，请检查 package-center.json")
		return nil, a.centerErr
	}
	if p.state.Policies == nil {
		p.state.Policies = make(map[string]versionPolicy)
	}
	if p.state.Batch != nil && (p.state.Batch.Phase == "running" || p.state.Batch.Phase == "queued") {
		p.state.Batch.Phase = "paused"
		for i := range p.state.Batch.Items {
			if p.state.Batch.Items[i].Phase == "running" {
				p.state.Batch.Items[i].Phase = "interrupted"
				p.state.Batch.Items[i].Message = "服务端重启；请核对设备版本后再继续"
			}
		}
	}
	a.center = p
	return p, nil
}
func (p *packageCenter) saveLocked() error {
	if len(p.state.History) > 200 {
		p.state.History = p.state.History[len(p.state.History)-200:]
	}
	return writePackageFile(p.path, p.state)
}
func writePackageFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePackageBytes(path, raw)
}
func writePackageBytes(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".package-")
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
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var packageSecretPattern = regexp.MustCompile(`(?i)(token|grant|key|authorization)[=:][^\s&"']+`)

func packageMessage(message string) string {
	message = packageSecretPattern.ReplaceAllString(message, "$1=[hidden]")
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500])
	}
	return message
}
func (p *packageCenter) record(device, name, kind string, state update.Status) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state.Message = packageMessage(state.Message)
	state.UpdatedAt = time.Now().UTC()
	if state.StartedAt.IsZero() {
		state.StartedAt = state.UpdatedAt
	}
	id := makeID() + makeID()
	op := packageOperation{ID: id, DeviceID: device, DeviceName: name, Kind: kind, State: state, Events: []packageEvent{{state.Phase, state.Message, state.UpdatedAt}}}
	p.state.History = append(p.state.History, op)
	if err := p.saveLocked(); err != nil {
		p.state.History = p.state.History[:len(p.state.History)-1]
		return "", err
	}
	return id, nil
}
func (p *packageCenter) progressLocked(index int, state update.Status) {
	op := &p.state.History[index]
	state.Message = packageMessage(state.Message)
	if state.StartedAt.IsZero() {
		state.StartedAt = op.State.StartedAt
	}
	state.UpdatedAt = time.Now().UTC()
	if state.Phase != op.State.Phase || state.Message != op.State.Message {
		op.Events = append(op.Events, packageEvent{state.Phase, state.Message, state.UpdatedAt})
		if len(op.Events) > 24 {
			op.Events = op.Events[len(op.Events)-24:]
		}
	}
	op.State = state
}
func (p *packageCenter) progress(id string, state update.Status) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.state.History {
		if p.state.History[i].ID == id {
			p.progressLocked(i, state)
			return p.saveLocked()
		}
	}
	return nil
}
func (p *packageCenter) locked(device string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Policies[device].Locked
}
func (a *app) reconcilePackageOperations(p *packageCenter) {
	statuses := a.hub.Statuses()
	devices := a.store.Snapshot().Devices
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := false
	for i, op := range p.state.History {
		if op.State.Phase == "succeeded" || op.State.Phase == "failed" || op.State.Phase == "expired" {
			continue
		}
		for _, device := range devices {
			status, online := statuses[device.ID]
			if device.ID == op.DeviceID && online && status.Since.After(op.State.StartedAt) && device.PackageVersion == op.State.ToVersion {
				state := op.State
				state.Phase, state.Message = "succeeded", "已核对安装版本，设备成功连接"
				p.progressLocked(i, state)
				changed = true
				break
			}
		}
		if p.state.History[i].State.Phase != "succeeded" && time.Since(op.State.StartedAt) > 15*time.Minute {
			state := op.State
			state.Phase, state.Message = "failed", "未在等待时间内确认设备连接；请查看路由器状态并重新检测"
			if op.State.Phase == "ready" {
				state.Phase, state.Message = "expired", "安装命令已过期，请重新生成"
			}
			p.progressLocked(i, state)
			changed = true
		}
	}
	if changed {
		_ = p.saveLocked()
	}
}
func decodePackageInput(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing data")
	}
	return nil
}
func (a *app) packageCenterHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/api/admin/package-center")
	switch {
	case r.Method == "GET" && operation == "":
		a.reconcilePackageOperations(p)
		catalog, downloads, packageError := a.packageCatalog(p)
		p.mu.Lock()
		raw, _ := json.Marshal(p.state)
		p.mu.Unlock()
		var view map[string]any
		_ = json.Unmarshal(raw, &view)
		view["profiles"] = append([]installProfile{standardProfile()}, p.profileSnapshot()...)
		view["catalog"], view["downloads"], view["packageError"] = catalog, downloads, packageError
		writeJSON(w, view)
	case r.Method == "PUT" && operation == "/profile":
		input := standardProfile()
		input.ID, input.Name = "", ""
		if decodePackageInput(w, r, &input) != nil || len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 40 || (input.InterfaceMode != "auto" && input.InterfaceMode != "select") || len(input.Server) > 512 {
			http.Error(w, "模板名称、出口设置或服务器地址无效", 400)
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			http.Error(w, "请输入模板名称", 400)
			return
		}
		if input.Server != "" {
			u, err := parsePublicURL(input.Server)
			if err != nil {
				http.Error(w, "请输入有效的服务器地址", 400)
				return
			}
			input.Server = strings.TrimRight(u.String(), "/")
		}
		p.mu.Lock()
		index := -1
		for i := range p.state.Profiles {
			if p.state.Profiles[i].ID == input.ID {
				index = i
			}
		}
		if input.ID != "" && index < 0 {
			p.mu.Unlock()
			http.Error(w, "模板不存在", 404)
			return
		}
		previous := append([]installProfile(nil), p.state.Profiles...)
		if index < 0 {
			if len(p.state.Profiles) >= 20 {
				p.mu.Unlock()
				http.Error(w, "最多保存 20 个安装模板", 400)
				return
			}
			input.ID = makeID()
			p.state.Profiles = append(p.state.Profiles, input)
		} else {
			p.state.Profiles[index] = input
		}
		err = p.saveLocked()
		if err != nil {
			p.state.Profiles = previous
		}
		p.mu.Unlock()
		if err != nil {
			http.Error(w, "模板保存失败", 500)
			return
		}
		writeJSON(w, input)
	case r.Method == "PUT" && operation == "/policy":
		var input struct {
			DeviceID string `json:"deviceId"`
			Locked   bool   `json:"locked"`
		}
		if decodePackageInput(w, r, &input) != nil {
			http.Error(w, "参数无效", 400)
			return
		}
		view, exists := a.pluginUpdateView(input.DeviceID)
		if !exists {
			http.NotFound(w, r)
			return
		}
		if view.State.Active() {
			http.Error(w, "更新完成后才能修改版本锁定", 409)
			return
		}
		a.updateMu.Lock()
		if a.deviceUpdates[input.DeviceID].State.Active() {
			a.updateMu.Unlock()
			http.Error(w, "更新完成后才能修改版本锁定", 409)
			return
		}
		p.mu.Lock()
		previous := p.state.Policies[input.DeviceID]
		policy := versionPolicy{input.Locked, view.InstalledVersion, time.Now().UTC()}
		p.state.Policies[input.DeviceID] = policy
		err = p.saveLocked()
		if err != nil {
			p.state.Policies[input.DeviceID] = previous
		}
		p.mu.Unlock()
		a.updateMu.Unlock()
		if err != nil {
			http.Error(w, "版本策略保存失败", 500)
			return
		}
		writeJSON(w, policy)
	case r.Method == "POST" && operation == "/profile/remove":
		var input struct {
			ID string `json:"id"`
		}
		if decodePackageInput(w, r, &input) != nil || input.ID == "standard" {
			http.Error(w, "无法删除内置模板", 400)
			return
		}
		p.mu.Lock()
		previous := append([]installProfile(nil), p.state.Profiles...)
		found := false
		for i, profile := range p.state.Profiles {
			if profile.ID == input.ID {
				p.state.Profiles = append(p.state.Profiles[:i], p.state.Profiles[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			p.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		err = p.saveLocked()
		if err != nil {
			p.state.Profiles = previous
		}
		p.mu.Unlock()
		if err != nil {
			http.Error(w, "模板删除失败", 500)
			return
		}
		w.WriteHeader(204)
	case r.Method == "POST" && operation == "/preflight":
		a.packagePreflightHTTP(w, r)
	case r.Method == "POST" && operation == "/batch":
		a.packageBatchHTTP(w, r, p)
	case r.Method == "POST" && operation == "/batch/control":
		a.packageBatchControl(w, r, p)
	case r.Method == "POST" && operation == "/rollback":
		a.packageRollbackHTTP(w, r, p)
	case r.Method == "GET" && operation == "/report":
		p.mu.Lock()
		var record *packageOperation
		for _, op := range p.state.History {
			if op.ID == r.URL.Query().Get("id") {
				copy := op
				record = &copy
			}
		}
		raw, _ := json.MarshalIndent(record, "", "  ")
		p.mu.Unlock()
		if record == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="RemoteGate-report-%s.json"`, record.ID))
		_, _ = w.Write(raw)
	default:
		http.Error(w, "请求方式或操作无效", 405)
	}
}
func (p *packageCenter) profileSnapshot() []installProfile {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]installProfile(nil), p.state.Profiles...)
}
func (p *packageCenter) profile(id string) (installProfile, bool) {
	if id == "" || id == "standard" {
		return standardProfile(), true
	}
	for _, profile := range p.profileSnapshot() {
		if profile.ID == id {
			return profile, true
		}
	}
	return installProfile{}, false
}

func compatiblePackage(m update.Manifest) bool { return m.AgentVersion == buildinfo.Version() }

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

func (p *packageCenter) latestUpdateState(device string) update.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.state.History) - 1; i >= 0; i-- {
		op := p.state.History[i]
		if op.DeviceID == device && op.Kind != "install" {
			return op.State
		}
	}
	return update.Status{Phase: "idle"}
}
func (p *packageCenter) rememberUpdateState(device string, state update.Status) {
	if state.ID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.state.History) - 1; i >= 0; i-- {
		op := p.state.History[i]
		if op.DeviceID == device && op.Kind != "install" && op.State.ID == state.ID {
			if op.State.Phase == "succeeded" || (op.State.Phase == state.Phase && op.State.Message == packageMessage(state.Message)) {
				return
			}
			p.progressLocked(i, state)
			_ = p.saveLocked()
			return
		}
		if op.DeviceID == device && op.Kind != "install" && op.State.Active() && op.State.ToVersion == state.ToVersion {
			p.progressLocked(i, state)
			_ = p.saveLocked()
			return
		}
	}
}
func (a *app) refreshPackageUpdate(ctx context.Context, id string) (pluginUpdateView, error) {
	view, exists := a.pluginUpdateView(id)
	if !exists {
		return view, errors.New("设备不存在")
	}
	if !view.CanUpdate {
		return view, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	response, err := a.hub.Request(ctx, id, protocol.Message{Type: "update-status", ID: makeID()})
	if err != nil || response.Error != "" {
		view.Error = "设备暂时无法响应，请稍后复检"
		if response.Error != "" {
			view.Error = packageMessage(response.Error)
		}
		return view, nil
	}
	var routerView pluginUpdateView
	if len(response.Body) > 16384 || json.Unmarshal(response.Body, &routerView) != nil {
		return view, errors.New("设备返回的更新状态无效")
	}
	view.RecoverySupported = routerView.RecoverySupported
	// Registration confirms the installed version even after the temporary router status is cleared.
	if routerView.InstalledVersion != "" {
		view.InstalledVersion = routerView.InstalledVersion
	}
	if routerView.State.Phase != "idle" || (!view.State.Active() && view.State.Phase != "succeeded") {
		if view.State.ID == routerView.State.ID && !view.State.StartedAt.IsZero() {
			routerView.State.StartedAt = view.State.StartedAt
			routerView.State.FromVersion = view.State.FromVersion
			routerView.State.ToVersion = view.State.ToVersion
		}
		view.State = routerView.State
	}
	if view.Manifest != nil {
		comparison, err := update.Compare(view.Manifest.PackageVersion, view.InstalledVersion)
		view.Available = err == nil && comparison > 0 && !view.Locked
	}
	return a.rememberPluginUpdate(id, view), nil
}
func (a *app) startPackageUpdate(ctx context.Context, id string, item packageArtifact, rollback bool) (pluginUpdateView, error) {
	view, exists := a.pluginUpdateView(id)
	if !exists || !view.Online || !view.CanUpdate {
		return view, errors.New("设备未上线或尚不支持在线更新")
	}
	if view.State.RollbackFailed {
		return view, errors.New("上次自动回滚未完成，请先在路由器恢复旧插件并核对本机备份")
	}
	if view.Locked {
		return view, errors.New("此设备已锁定版本，请先解除锁定")
	}
	comparison, err := update.Compare(item.Manifest.PackageVersion, view.InstalledVersion)
	if err != nil || (!rollback && comparison <= 0) || (rollback && (comparison >= 0 || !view.CanManageUpdate || !compatiblePackage(item.Manifest))) {
		return view, errors.New("目标版本不符合升级或兼容回退要求")
	}
	p, err := a.packageCenterStore()
	if err != nil {
		return view, err
	}
	approved := item.Manifest
	if view.CanManageUpdate {
		approved, err = a.issuePackageGrant(p, id, item, rollback)
		if err != nil {
			return view, err
		}
	} else if current, err := a.loadUpdatePackage(); err != nil || current.SHA256 != item.Manifest.SHA256 {
		return view, errors.New("旧插件仅支持更新到当前通用安装包，请先升级插件")
	}
	state := update.Status{AutoRollback: view.RecoverySupported, ID: makeID() + makeID(), Phase: "checking", FromVersion: view.InstalledVersion, ToVersion: item.Manifest.PackageVersion, Message: "正在准备更新并等待设备确认", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	a.updateMu.Lock()
	if p.locked(id) {
		a.updateMu.Unlock()
		return view, errors.New("此设备已锁定版本，请先解除锁定")
	}
	if view.State.Active() || a.deviceUpdates[id].State.Active() {
		a.updateMu.Unlock()
		return view, errors.New("已有插件更新正在进行")
	}
	if a.deviceUpdates == nil {
		a.deviceUpdates = make(map[string]pluginUpdateView)
	}
	view.State = state
	a.deviceUpdates[id] = view
	a.updateMu.Unlock()
	name := id
	for _, device := range a.store.Snapshot().Devices {
		if device.ID == id {
			name = device.Name
		}
	}
	kind := "update"
	if rollback {
		kind = "rollback"
	}
	operationID, err := p.record(id, name, kind, state)
	if err != nil {
		state.Phase, state.Message = "failed", "更新记录保存失败，任务未发起"
		view.State = state
		return a.rememberPluginUpdate(id, view), err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body, _ := json.Marshal(approved)
	response, err := a.hub.Request(ctx, id, protocol.Message{Type: "update-start", ID: makeID(), Deadline: time.Now().Add(18 * time.Second).UnixMilli(), Body: body})
	if err != nil || response.Error != "" {
		state.Phase, state.Message = "checking", "更新请求未获确认，正在核对设备实际状态"
		if response.Error != "" {
			state.Phase = "failed"
			state.Message = packageMessage(response.Error)
		}
		view.State = state
		_ = p.progress(operationID, state)
		a.rememberPluginUpdate(id, view)
		return view, errors.New(state.Message)
	}
	var routerView pluginUpdateView
	validPhase := map[string]bool{"checking": true, "downloading": true, "backing_up": true, "installing": true, "restarting": true, "verifying": true, "rolling_back": true, "succeeded": true, "failed": true}
	if len(response.Body) > 16384 || json.Unmarshal(response.Body, &routerView) != nil || routerView.State.ID == "" || len(routerView.State.ID) > 64 || !validPhase[routerView.State.Phase] {
		state.Phase, state.Message = "failed", "设备返回的更新状态无效，请重新检测"
		view.State = state
		_ = p.progress(operationID, state)
		return a.rememberPluginUpdate(id, view), errors.New(state.Message)
	}
	routerView.State.StartedAt, routerView.State.FromVersion, routerView.State.ToVersion = state.StartedAt, state.FromVersion, state.ToVersion
	view.State = routerView.State
	view.RecoverySupported = routerView.RecoverySupported
	view.Manifest = &item.Manifest
	_ = p.progress(operationID, view.State)
	return a.rememberPluginUpdate(id, view), nil
}
func (a *app) packagePreflightHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DeviceID string `json:"deviceId"`
	}
	if decodePackageInput(w, r, &input) != nil {
		http.Error(w, "参数无效", 400)
		return
	}
	view, exists := a.pluginUpdateView(input.DeviceID)
	if !exists || !view.Online || !view.CanManageUpdate {
		http.Error(w, "设备需在线并升级到支持安装检查的插件版本", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	response, err := a.hub.Request(ctx, input.DeviceID, protocol.Message{Type: "update-preflight", ID: makeID()})
	var result update.Preflight
	if err != nil || response.Error != "" || len(response.Body) > 16384 || json.Unmarshal(response.Body, &result) != nil || len(result.Checks) == 0 {
		http.Error(w, "设备检查未完成，请确认连接后重试", 502)
		return
	}
	writeJSON(w, result)
}
func (a *app) packageRollbackHTTP(w http.ResponseWriter, r *http.Request, p *packageCenter) {
	var input struct {
		DeviceID string `json:"deviceId"`
		Version  string `json:"version"`
	}
	if decodePackageInput(w, r, &input) != nil {
		http.Error(w, "参数无效", 400)
		return
	}
	arch := ""
	for _, device := range a.store.Snapshot().Devices {
		if device.ID == input.DeviceID {
			arch = update.Architecture(device.Info.Arch)
		}
	}
	if arch == "" {
		http.Error(w, "设备尚未上报受支持的 CPU 架构", 409)
		return
	}
	item, err := a.packageArtifact(p, input.Version, arch)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	view, err := a.startPackageUpdate(r.Context(), input.DeviceID, item, true)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeJSON(w, view)
}
func (a *app) packageBatchHTTP(w http.ResponseWriter, r *http.Request, p *packageCenter) {
	var input struct {
		DeviceIDs     []string `json:"deviceIds"`
		StopOnFailure bool     `json:"stopOnFailure"`
	}
	if decodePackageInput(w, r, &input) != nil || len(input.DeviceIDs) == 0 || len(input.DeviceIDs) > 50 {
		http.Error(w, "请选择 1–50 台设备", 400)
		return
	}
	m, err := a.loadUpdatePackage()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	job := &packageBatch{ID: makeID() + makeID(), Phase: "queued", Version: m.PackageVersion, SHA256: m.SHA256, StopOnFailure: input.StopOnFailure, StartedAt: time.Now().UTC()}
	seen := map[string]bool{}
	for _, id := range input.DeviceIDs {
		view, exists := a.pluginUpdateView(id)
		if !exists || !view.Online || !view.CanUpdate || view.Locked || !view.Available || view.State.Active() || seen[id] {
			http.Error(w, "选择中包含离线、锁定、正在更新或无需更新的设备，请重新检查", 409)
			return
		}
		seen[id] = true
		name := id
		for _, d := range a.store.Snapshot().Devices {
			if d.ID == id {
				name = d.Name
			}
		}
		job.Items = append(job.Items, packageBatchItem{id, name, "pending", "等待前一台设备完成"})
	}
	p.mu.Lock()
	if p.batchWorker || (p.state.Batch != nil && p.state.Batch.Phase == "paused") {
		p.mu.Unlock()
		http.Error(w, "已有批量任务，请完成或取消后再创建", 409)
		return
	}
	previous := p.state.Batch
	p.state.Batch = job
	err = p.saveLocked()
	if err != nil {
		p.state.Batch = previous
	}
	p.mu.Unlock()
	if err != nil {
		http.Error(w, "任务保存失败", 500)
		return
	}
	writeJSON(w, job)
	a.launchPackageBatch(p, job.ID)
}
func (a *app) packageBatchControl(w http.ResponseWriter, r *http.Request, p *packageCenter) {
	var input struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if decodePackageInput(w, r, &input) != nil || (input.Action != "pause" && input.Action != "resume" && input.Action != "cancel") {
		http.Error(w, "操作无效", 400)
		return
	}
	p.mu.Lock()
	job := p.state.Batch
	if job == nil || job.ID != input.ID {
		p.mu.Unlock()
		http.Error(w, "任务已变化", 409)
		return
	}
	switch input.Action {
	case "pause":
		job.Phase = "paused"
	case "cancel":
		job.Phase = "cancelled"
		for i := range job.Items {
			if job.Items[i].Phase == "pending" {
				job.Items[i].Phase, job.Items[i].Message = "cancelled", "尚未发起的任务已取消"
			}
		}
	case "resume":
		if job.Phase != "paused" {
			p.mu.Unlock()
			http.Error(w, "任务未暂停", 409)
			return
		}
		job.Phase = "queued"
	}
	err := p.saveLocked()
	raw, _ := json.Marshal(job)
	p.mu.Unlock()
	if err != nil {
		http.Error(w, "操作保存失败", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
	if input.Action == "resume" {
		a.launchPackageBatch(p, input.ID)
	}
}
func (a *app) launchPackageBatch(p *packageCenter, id string) {
	p.mu.Lock()
	if p.batchWorker {
		p.mu.Unlock()
		return
	}
	p.batchWorker = true
	p.mu.Unlock()
	go func() {
		defer func() {
			p.mu.Lock()
			p.batchWorker = false
			resume := p.state.Batch != nil && p.state.Batch.ID == id && p.state.Batch.Phase == "queued"
			p.mu.Unlock()
			if resume {
				a.launchPackageBatch(p, id)
			}
		}()
		for {
			p.mu.Lock()
			job := p.state.Batch
			if job == nil || job.ID != id || (job.Phase != "running" && job.Phase != "queued") {
				p.mu.Unlock()
				return
			}
			index := -1
			for i := range job.Items {
				if job.Items[i].Phase == "pending" {
					index = i
					break
				}
			}
			if index < 0 {
				job.Phase = "completed"
				_ = p.saveLocked()
				p.mu.Unlock()
				return
			}
			job.Phase = "running"
			job.Items[index].Phase, job.Items[index].Message = "running", "检查并更新设备"
			deviceID, version, frozenHash := job.Items[index].DeviceID, job.Version, job.SHA256
			_ = p.saveLocked()
			p.mu.Unlock()
			arch := ""
			status := a.hub.Statuses()[deviceID]
			if status.CanManageUpdate {
				for _, d := range a.store.Snapshot().Devices {
					if d.ID == deviceID {
						arch = update.Architecture(d.Info.Arch)
					}
				}
			}
			base, err := a.packageArtifact(p, version, "")
			if err == nil && base.Manifest.SHA256 != frozenHash {
				err = errors.New("队列安装包已变化，请重新创建任务")
			}
			var item packageArtifact
			if err == nil {
				item, err = a.packageArtifact(p, version, arch)
			}
			if err == nil {
				_, err = a.startPackageUpdate(context.Background(), deviceID, item, false)
			}
			if err == nil {
				err = a.waitPackageUpdate(deviceID, version)
			}
			p.mu.Lock()
			job = p.state.Batch
			if job == nil || job.ID != id {
				p.mu.Unlock()
				return
			}
			job.Items[index].Phase, job.Items[index].Message = "succeeded", "更新完成，设备已重新连接"
			if err != nil {
				job.Items[index].Phase, job.Items[index].Message = "failed", packageMessage(err.Error())
				if job.StopOnFailure && job.Phase == "running" {
					job.Phase = "paused"
				}
			}
			_ = p.saveLocked()
			p.mu.Unlock()
		}
	}()
}
func (a *app) waitPackageUpdate(id, version string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		view, _ := a.refreshPackageUpdate(context.Background(), id)
		if view.Online && view.State.Phase == "succeeded" {
			status := a.hub.Statuses()[id]
			for _, device := range a.store.Snapshot().Devices {
				if device.ID == id && device.PackageVersion == version && status.Since.After(view.State.StartedAt) {
					return nil
				}
			}
		}
		if view.State.Phase == "failed" {
			return errors.New(view.State.Message)
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("等待更新后重新连接超时，请核对实际安装版本")
}
func (a *app) maintainPackageOperations(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p, err := a.packageCenterStore()
			if err != nil {
				continue
			}
			a.reconcilePackageOperations(p)
			p.mu.Lock()
			ids := map[string]bool{}
			for _, op := range p.state.History {
				if op.Kind != "install" && op.State.Active() {
					ids[op.DeviceID] = true
				}
			}
			p.mu.Unlock()
			for id := range ids {
				if a.hub.Statuses()[id].CanUpdate {
					_, _ = a.refreshPackageUpdate(ctx, id)
				}
			}
		}
	}
}

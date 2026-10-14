package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/update"
)

type updatePaths struct{ StateDir, Configuration, Version, Service string }

var routerUpdatePaths = updatePaths{"/var/run/remotegate-update", "/etc/config/remotegate", "/usr/share/remotegate/version", "/etc/init.d/remotegate"}

type updateView struct {
	InstalledVersion string           `json:"installedVersion"`
	Manifest         *update.Manifest `json:"manifest,omitempty"`
	Available        bool             `json:"available"`
	State            update.Status    `json:"state"`
}

type updatePlan struct {
	Config   Config          `json:"config"`
	Manifest update.Manifest `json:"manifest"`
	State    update.Status   `json:"state"`
}

func installedPackage(paths updatePaths) string {
	raw, _ := os.ReadFile(paths.Version)
	return strings.TrimSpace(string(raw))
}

func routerUpdateSupported() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	if _, err := os.Stat(routerUpdatePaths.Configuration); err != nil {
		return false
	}
	_, err := exec.LookPath("opkg")
	return err == nil
}

func readUpdateState(paths updatePaths) update.Status {
	state := update.Status{Phase: "idle"}
	raw, err := os.ReadFile(filepath.Join(paths.StateDir, "status.json"))
	if err == nil && len(raw) <= 8192 {
		_ = json.Unmarshal(raw, &state)
	}
	if state.Phase == "" {
		state.Phase = "idle"
	}
	if state.Active() == false && (state.Phase == "checking" || state.Phase == "downloading" || state.Phase == "installing" || state.Phase == "restarting") {
		state.Phase, state.Message = "failed", "更新超时，请检查路由器日志后重试"
	}
	return state
}

func saveUpdateState(paths updatePaths, state update.Status) error {
	if err := os.MkdirAll(paths.StateDir, 0700); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	if len(state.Message) > 1500 {
		state.Message = state.Message[:1500]
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(paths.StateDir, ".status-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(paths.StateDir, "status.json"))
}

func updateBase(cfg Config) (*url.URL, error) {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("服务端地址无效")
	}
	if u.Scheme == "https" {
		if cfg.InsecureTLS {
			return nil, errors.New("在线更新需要有效的 HTTPS 证书，请关闭跳过证书验证")
		}
	} else if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !(ip.IsPrivate() || ip.IsLoopback())) {
			return nil, errors.New("公网在线更新请使用 HTTPS 服务端地址")
		}
	} else {
		return nil, errors.New("在线更新仅支持 HTTP/HTTPS 服务端地址")
	}
	if cfg.DeviceID == "" || cfg.Token == "" {
		return nil, errors.New("请先保存设备连接配置")
	}
	return u, nil
}

func updateClient(cfg Config) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: boundDialer(cfg.Interface, cfg.Mark).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func updateRequest(ctx context.Context, cfg Config, path string) (*http.Response, error) {
	u, err := updateBase(cfg)
	if err != nil {
		return nil, err
	}
	resource, err := url.Parse(path)
	if err != nil || resource.IsAbs() || resource.Host != "" || !strings.HasPrefix(resource.Path, "/api/agent/update") {
		return nil, errors.New("更新资源路径无效")
	}
	u.Path, u.RawQuery = resource.Path, resource.RawQuery
	query := u.Query()
	query.Set("arch", runtime.GOARCH)
	query.Set("tools", "1")
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("X-RemoteGate-Device-ID", cfg.DeviceID)
	client := updateClient(cfg)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("服务端更新资源不可用（HTTP %d）：%s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

func fetchUpdateManifest(ctx context.Context, cfg Config) (update.Manifest, error) {
	return fetchUpdateManifestTarget(ctx, cfg, update.Manifest{})
}

func updateResource(resource string, m update.Manifest) string {
	path := "/api/agent/update" + resource
	if m.Artifact != "" && m.Grant != "" {
		path += "?" + url.Values{"artifact": {m.Artifact}, "grant": {m.Grant}}.Encode()
	}
	return path
}

func fetchUpdateManifestTarget(ctx context.Context, cfg Config, expected update.Manifest) (update.Manifest, error) {
	var manifest update.Manifest
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	resp, err := updateRequest(ctx, cfg, updateResource("", expected))
	if err != nil {
		return manifest, err
	}
	defer resp.Body.Close()
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&manifest); err != nil {
		return manifest, errors.New("无法读取服务端更新清单，请先更新服务端")
	}
	if err = manifest.Validate(); err != nil {
		return manifest, err
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "ARM64", "arm": "ARMv7"}[runtime.GOARCH]
	for _, value := range manifest.Architectures {
		if arch != "" && arch == value {
			return manifest, nil
		}
	}
	return manifest, errors.New("此安装包不支持路由器的 CPU 架构")
}

func checkAgentUpdate(ctx context.Context, cfg Config, paths updatePaths) (updateView, error) {
	view := updateView{InstalledVersion: installedPackage(paths), State: readUpdateState(paths)}
	manifest, err := fetchUpdateManifest(ctx, cfg)
	if err != nil {
		return view, err
	}
	view.Manifest = &manifest
	comparison, err := update.Compare(manifest.PackageVersion, view.InstalledVersion)
	if err != nil {
		return view, err
	}
	view.Available = comparison > 0
	return view, nil
}

func acquireUpdate(paths updatePaths) error {
	if err := os.MkdirAll(paths.StateDir, 0700); err != nil {
		return err
	}
	lock := filepath.Join(paths.StateDir, "lock")
	if err := os.Mkdir(lock, 0700); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(lock)
	if err != nil || !info.IsDir() || time.Since(info.ModTime()) < 10*time.Minute {
		return errors.New("已有插件更新正在执行，请稍候")
	}
	// Only our empty lock directory is removed; never follow a link or delete arbitrary files.
	if err = os.Remove(lock); err != nil {
		return errors.New("更新锁无法恢复，请检查路由器")
	}
	return os.Mkdir(lock, 0700)
}

func releaseUpdate(paths updatePaths) { _ = os.Remove(filepath.Join(paths.StateDir, "lock")) }

func startAgentUpdate(ctx context.Context, cfg Config, expected update.Manifest) (updateView, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return updateView{}, errors.New("在线更新需要在 OpenWrt 路由器以 root 运行")
	}
	view := updateView{InstalledVersion: installedPackage(routerUpdatePaths), State: readUpdateState(routerUpdatePaths)}
	manifest, err := fetchUpdateManifestTarget(ctx, cfg, expected)
	view.Manifest = &manifest
	if err != nil {
		return view, err
	}
	comparison, err := update.Compare(manifest.PackageVersion, view.InstalledVersion)
	if err != nil {
		return view, err
	}
	view.Available = comparison > 0
	if expected.PackageVersion != manifest.PackageVersion || expected.SHA256 != manifest.SHA256 || expected.Rollback != manifest.Rollback {
		return view, errors.New("服务端安装包已变化，请重新检查更新")
	}
	if manifest.Rollback {
		if comparison >= 0 || strings.Split(view.InstalledVersion, "-")[0] != manifest.AgentVersion {
			return view, errors.New("回退目标必须是客户端版本相同的较早安装包")
		}
	} else if !view.Available {
		return view, errors.New("当前插件已是此服务器提供的版本，或版本更高")
	}
	if _, err = exec.LookPath("opkg"); err != nil {
		return view, errors.New("当前系统不支持 opkg 安装")
	}
	if _, err = os.Stat(routerUpdatePaths.Configuration); err != nil {
		return view, errors.New("未找到现有插件配置，请先安装 LuCI 插件")
	}
	for _, path := range []string{os.TempDir(), filepath.Dir(routerUpdatePaths.Configuration)} {
		free, err := availableStorage(path)
		if err != nil || free < uint64(manifest.Size)*4+(16<<20) {
			return view, errors.New("路由器可用空间不足，请先清理空间")
		}
	}
	if err = acquireUpdate(routerUpdatePaths); err != nil {
		return view, err
	}
	if err = ctx.Err(); err != nil {
		releaseUpdate(routerUpdatePaths)
		return view, err
	}
	release := true
	defer func() {
		if release {
			releaseUpdate(routerUpdatePaths)
		}
	}()
	dir, err := os.MkdirTemp("", "remotegate-update-")
	if err != nil {
		return view, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return view, err
	}
	view.State = update.Status{ID: hex.EncodeToString(key), Phase: "checking", FromVersion: view.InstalledVersion, ToVersion: view.Manifest.PackageVersion, Message: "正在准备插件更新", StartedAt: time.Now().UTC()}
	plan, err := json.Marshal(updatePlan{Config: cfg, Manifest: *view.Manifest, State: view.State})
	if err != nil {
		return view, err
	}
	if err = os.WriteFile(filepath.Join(dir, "plan.json"), plan, 0600); err != nil {
		return view, err
	}
	executable, err := os.Executable()
	if err != nil {
		return view, err
	}
	source, err := os.Open(executable)
	if err != nil {
		return view, err
	}
	worker := filepath.Join(dir, "remotegate-update-worker")
	target, err := os.OpenFile(worker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		source.Close()
		return view, err
	}
	_, copyErr := io.Copy(target, source)
	source.Close()
	closeErr := target.Close()
	if copyErr != nil {
		return view, copyErr
	}
	if closeErr != nil {
		return view, closeErr
	}
	if err = saveUpdateState(routerUpdatePaths, view.State); err != nil {
		return view, err
	}
	if err = ctx.Err(); err != nil {
		view.State.Phase, view.State.Message = "failed", "更新请求超时，请重新发起"
		_ = saveUpdateState(routerUpdatePaths, view.State)
		return view, err
	}
	command := exec.Command(worker, "--update-worker", dir)
	detachUpdateProcess(command)
	if err = command.Start(); err != nil {
		view.State.Phase = "failed"
		view.State.Message = err.Error()
		_ = saveUpdateState(routerUpdatePaths, view.State)
		return view, err
	}
	go func() { _ = command.Wait() }()
	release, cleanup = false, false
	return view, nil
}

func downloadUpdate(ctx context.Context, cfg Config, manifest update.Manifest, destination string) error {
	resp, err := updateRequest(ctx, cfg, updateResource("/package", manifest))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, manifest.Size+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != manifest.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return errors.New("安装包完整性校验失败，已停止更新")
	}
	return nil
}

func performUpdate(ctx context.Context, plan updatePlan, dir string, paths updatePaths, install func(context.Context, string) error, restart func(bool) error, wasEnabled bool) (result error) {
	state := plan.State
	report := func(phase, message string) {
		state.Phase, state.Message = phase, message
		_ = saveUpdateState(paths, state)
	}
	defer func() {
		if result != nil {
			report("failed", result.Error())
		}
	}()
	if err := plan.Manifest.Validate(); err != nil {
		return err
	}
	comparison, err := update.Compare(plan.Manifest.PackageVersion, installedPackage(paths))
	if err != nil {
		return err
	}
	if plan.Manifest.Rollback {
		if comparison >= 0 || strings.Split(installedPackage(paths), "-")[0] != plan.Manifest.AgentVersion {
			return errors.New("回退目标与当前客户端不兼容")
		}
	} else if comparison <= 0 {
		return errors.New("更新目标不是更高版本，已停止更新")
	}
	configuration, err := os.ReadFile(paths.Configuration)
	if err != nil {
		return errors.New("无法备份现有连接配置，已停止更新")
	}
	if err = os.WriteFile(filepath.Join(dir, "configuration.backup"), configuration, 0600); err != nil {
		return err
	}
	report("downloading", "正在下载并校验安装包")
	packagePath := filepath.Join(dir, "package.run")
	if err = downloadUpdate(ctx, plan.Config, plan.Manifest, packagePath); err != nil {
		return err
	}
	report("installing", "正在安装插件；连接会暂时中断")
	installErr := install(ctx, packagePath)
	// Restore the exact UCI configuration even if opkg changed it or installation failed.
	if err = os.WriteFile(paths.Configuration, configuration, 0600); err != nil {
		return fmt.Errorf("连接配置恢复失败：%w", err)
	}
	_ = os.Chmod(paths.Configuration, 0600)
	report("restarting", "正在恢复连接配置并重启客户端")
	restartErr := restart(wasEnabled)
	if installErr != nil {
		return fmt.Errorf("安装失败，连接配置已保留：%w", installErr)
	}
	if restartErr != nil {
		return fmt.Errorf("插件已安装，客户端重启失败：%w", restartErr)
	}
	if installedPackage(paths) != plan.Manifest.PackageVersion {
		return errors.New("安装后的插件版本与更新目标不一致，请手动检查")
	}
	report("succeeded", "插件更新完成")
	return nil
}

func runUpdateWorker(dir string) error {
	// The copied helper survives opkg stopping and replacing the running agent.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("更新执行器仅支持路由器 root 环境")
	}
	dir = filepath.Clean(dir)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || filepath.Dir(dir) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(dir), "remotegate-update-") {
		return errors.New("更新执行器目录无效")
	}
	defer os.RemoveAll(dir)
	defer releaseUpdate(routerUpdatePaths)
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		_ = saveUpdateState(routerUpdatePaths, update.Status{Phase: "failed", Message: "无法读取更新计划，请重新发起"})
		return err
	}
	var plan updatePlan
	if len(raw) > 32768 || json.Unmarshal(raw, &plan) != nil {
		_ = saveUpdateState(routerUpdatePaths, update.Status{Phase: "failed", Message: "更新计划无效，请重新发起"})
		return errors.New("更新计划无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	wasEnabled := exec.Command(routerUpdatePaths.Service, "enabled").Run() == nil
	return performUpdate(ctx, plan, dir, routerUpdatePaths, func(ctx context.Context, path string) error {
		command := exec.CommandContext(ctx, "/bin/sh", path, "--update")
		if plan.Manifest.Rollback {
			_, payload, err := update.PackagePayload(path, runtime.GOARCH)
			if err != nil {
				return err
			}
			ipk := filepath.Join(dir, "rollback.ipk")
			if err = os.WriteFile(ipk, payload, 0600); err != nil {
				return err
			}
			command = exec.CommandContext(ctx, "opkg", "--force-downgrade", "--force-reinstall", "install", ipk)
		}
		prepareUpdateInstaller(command)
		logFile, err := os.OpenFile(filepath.Join(routerUpdatePaths.StateDir, "install.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer logFile.Close()
		command.Stdout, command.Stderr = logFile, logFile
		return command.Run()
	}, func(enabled bool) error {
		verb := "disable"
		if enabled {
			verb = "enable"
		}
		if err := exec.CommandContext(ctx, routerUpdatePaths.Service, verb).Run(); err != nil {
			return err
		}
		return exec.CommandContext(ctx, routerUpdatePaths.Service, "restart").Run()
	}, wasEnabled)
}

func updateControl(ctx context.Context, cfg Config, msg protocol.Message) protocol.Message {
	response := protocol.Message{Type: "response", ID: msg.ID, Status: 200}
	if msg.Type == "update-preflight" {
		response.Body, _ = json.Marshal(agentPreflight(ctx, cfg, routerUpdatePaths))
		return response
	}
	view := updateView{InstalledVersion: installedPackage(routerUpdatePaths), State: readUpdateState(routerUpdatePaths)}
	if msg.Type == "update-start" {
		var expected update.Manifest
		if msg.Deadline == 0 || time.Now().UnixMilli() > msg.Deadline || len(msg.Body) > 8192 || json.Unmarshal(msg.Body, &expected) != nil {
			response.Error = "更新请求已过期或无效，请重新发起"
			return response
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(msg.Deadline))
		defer cancel()
		var err error
		view, err = startAgentUpdate(ctx, cfg, expected)
		if err != nil {
			response.Error = err.Error()
			return response
		}
	}
	response.Body, _ = json.Marshal(view)
	return response
}

func handleUpdateCLI(args []string) bool {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--update-") {
		return false
	}
	var value any
	var err error
	if args[0] == "--update-worker" {
		if len(args) != 2 {
			return true
		}
		if err = runUpdateWorker(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return true
	}
	if args[0] == "--update-status" {
		value = updateView{InstalledVersion: installedPackage(routerUpdatePaths), State: readUpdateState(routerUpdatePaths)}
	} else {
		if (args[0] != "--update-check" && args[0] != "--update-start") || (args[0] == "--update-start" && len(args) != 4) || (args[0] == "--update-check" && len(args) != 2) {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": "更新参数无效，请刷新插件页面"})
			return true
		}
		path := "/var/run/remotegate/agent.json"
		if len(args) > 1 {
			path = args[1]
		}
		var cfg Config
		raw, e := os.ReadFile(path)
		if e != nil || len(raw) > 32768 || json.Unmarshal(raw, &cfg) != nil {
			err = errors.New("请先保存并启用插件连接配置")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var view updateView
			if args[0] == "--update-start" {
				view, err = startAgentUpdate(ctx, cfg, update.Manifest{PackageVersion: args[2], SHA256: args[3]})
			} else {
				view, err = checkAgentUpdate(ctx, cfg, routerUpdatePaths)
			}
			value = view
		}
	}
	if err != nil {
		value = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(os.Stdout).Encode(value)
	return true
}

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/local/remotegate/internal/update"
)

func agentPreflight(ctx context.Context, cfg Config, paths updatePaths) update.Preflight {
	result := update.Preflight{OK: true, Architecture: update.ArchitectureLabel(runtime.GOARCH)}
	check := func(key, label string, ok bool, detail string) {
		result.OK = result.OK && ok
		result.Checks = append(result.Checks, update.Check{Key: key, Label: label, OK: ok, Detail: detail})
	}
	check("platform", "系统与权限", runtime.GOOS == "linux" && os.Geteuid() == 0, "需要 Linux 路由器上的 root 权限")
	_, err := exec.LookPath("opkg")
	check("manager", "安装工具", err == nil, "需要 opkg；不支持 APK 包管理器")
	_, err = os.Stat(paths.Configuration)
	check("configuration", "原连接配置", err == nil, "升级时保留设备 ID、令牌及连接设置")
	_, err = updateBase(cfg)
	detail := "连接地址与 HTTPS 校验正常"
	if err != nil {
		detail = err.Error()
	}
	check("transport", "下载地址", err == nil, detail)
	manifest, err := fetchUpdateManifest(ctx, cfg)
	detail = "已连接服务器并读取匹配架构的安装包"
	if err != nil {
		detail = err.Error()
	}
	check("server", "服务器与架构", err == nil, detail)
	if err == nil {
		for _, path := range []string{os.TempDir(), filepath.Dir(paths.Configuration)} {
			free, err := availableStorage(path)
			needed := uint64(manifest.Size)*4 + (16 << 20)
			check("space:"+path, "可用空间 · "+path, err == nil && free >= needed, fmt.Sprintf("可用 %.1f MB，预计需要 %.1f MB", float64(free)/(1<<20), float64(needed)/(1<<20)))
		}
	}
	state := readUpdateState(paths)
	check("task", "更新任务", !state.Active(), "同一设备每次只执行一个更新任务")
	return result
}

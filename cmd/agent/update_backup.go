package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/remotegate/internal/update"
)

// Rebuild the installed package from its actual files, including opkg scripts.
// A server's historical download need not match what is installed on this router.
type updateBackup struct {
	Package, SHA256, AgentSHA256 string
}

func fileSHA256(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, update.MaxPackageSize+1))
	if err != nil {
		return "", err
	}
	if n > update.MaxPackageSize {
		return "", errors.New("文件超过更新大小限制")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func pluginFile(name string) bool {
	return name == "/etc/config/remotegate" || name == "/etc/init.d/remotegate" ||
		name == "/usr/sbin/remotegate-agent" || strings.HasPrefix(name, "/usr/libexec/remotegate-") ||
		strings.HasPrefix(name, "/usr/share/remotegate/") ||
		name == "/usr/lib/lua/luci/controller/remotegate.lua" ||
		name == "/usr/lib/lua/luci/model/cbi/remotegate.lua" ||
		strings.HasPrefix(name, "/usr/lib/lua/luci/view/remotegate/")
}

type backupEntry struct {
	name, source string
	mode         int64
	size         int64
}

func regularBackupFile(root, name string) (backupEntry, error) {
	if !strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") {
		return backupEntry{}, errors.New("插件文件路径无效")
	}
	// Reject symlinks in both the file and its parents. No device nodes or links
	// are allowed in a recovery archive which opkg will later extract as root.
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	current := root
	var info os.FileInfo
	for _, part := range parts {
		current = filepath.Join(current, part)
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return backupEntry{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return backupEntry{}, errors.New("插件文件含符号链接，无法安全备份")
		}
	}
	if !info.Mode().IsRegular() || info.Size() > update.MaxPackageSize {
		return backupEntry{}, errors.New("插件文件类型或大小异常")
	}
	return backupEntry{name: strings.TrimPrefix(name, "/"), source: current, mode: int64(info.Mode().Perm()), size: info.Size()}, nil
}

func writeBackupArchive(destination string, entries []backupEntry) (err error) {
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(destination)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err = tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: e.size, Typeflag: tar.TypeReg}); err != nil {
			break
		}
		var source *os.File
		source, err = os.Open(e.source)
		if err != nil {
			break
		}
		_, err = io.CopyN(tw, source, e.size)
		_ = source.Close()
		if err != nil {
			break
		}
	}
	for _, close := range []func() error{tw.Close, gz.Close, f.Sync, f.Close} {
		if e := close(); err == nil {
			err = e
		}
	}
	return err
}

func backupInstalledPlugin(root, dir, version string, packageSize int64) (updateBackup, error) {
	var backup updateBackup
	infoDir := filepath.Join(root, "usr/lib/opkg/info")
	prefix := filepath.Join(infoDir, "luci-app-remotegate")
	control, err := os.ReadFile(prefix + ".control")
	if err != nil || len(control) > 32768 {
		return backup, errors.New("无法读取旧插件安装信息，已停止更新")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(control), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	if fields["Package"] != "luci-app-remotegate" || fields["Version"] != version {
		return backup, errors.New("旧插件版本与安装记录不一致，已停止更新")
	}
	list, err := os.ReadFile(prefix + ".list")
	if err != nil || len(list) > 65536 {
		return backup, errors.New("无法读取旧插件文件清单，已停止更新")
	}
	var data, controls []backupEntry
	seen := map[string]bool{}
	var total int64
	for _, name := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		if !pluginFile(name) || seen[name] {
			return backup, errors.New("旧插件清单含异常路径，已停止更新")
		}
		seen[name] = true
		entry, err := regularBackupFile(root, name)
		if err != nil {
			return backup, fmt.Errorf("旧插件文件备份失败：%w", err)
		}
		total += entry.size
		if total > update.MaxPackageSize {
			return backup, errors.New("旧插件备份过大，已停止更新")
		}
		data = append(data, entry)
	}
	for _, required := range []string{"/usr/sbin/remotegate-agent", "/etc/init.d/remotegate", "/etc/config/remotegate", "/usr/share/remotegate/version"} {
		if !seen[required] {
			return backup, errors.New("旧插件文件不完整，已停止更新")
		}
	}
	for _, name := range []string{"control", "conffiles", "preinst", "postinst", "prerm", "postrm"} {
		entry, err := regularBackupFile(root, "/usr/lib/opkg/info/luci-app-remotegate."+name)
		if os.IsNotExist(err) && name != "control" {
			continue
		}
		if err != nil {
			return backup, err
		}
		if entry.size > 65536 {
			return backup, errors.New("旧插件安装脚本过大")
		}
		entry.name = name
		controls = append(controls, entry)
	}
	if free, err := availableStorage(dir); err != nil || free < uint64(total*2+packageSize*4+(16<<20)) {
		return backup, errors.New("空间不足以同时保留旧插件备份和安装新版，已停止更新")
	}
	defer os.Remove(filepath.Join(dir, "data.tar.gz"))
	defer os.Remove(filepath.Join(dir, "control.tar.gz"))
	for _, item := range []struct {
		name    string
		entries []backupEntry
	}{{"data.tar.gz", data}, {"control.tar.gz", controls}} {
		if err = writeBackupArchive(filepath.Join(dir, item.name), item.entries); err != nil {
			return backup, err
		}
	}
	if err = os.WriteFile(filepath.Join(dir, "debian-binary"), []byte("2.0\n"), 0600); err != nil {
		return backup, err
	}
	defer os.Remove(filepath.Join(dir, "debian-binary"))
	var outer []backupEntry
	for _, name := range []string{"debian-binary", "control.tar.gz", "data.tar.gz"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return backup, err
		}
		outer = append(outer, backupEntry{name, filepath.Join(dir, name), 0600, fi.Size()})
	}
	backup.Package = filepath.Join(dir, "previous.ipk")
	if err = writeBackupArchive(backup.Package, outer); err != nil {
		return backup, err
	}
	if backup.SHA256, err = fileSHA256(backup.Package); err != nil {
		return backup, err
	}
	backup.AgentSHA256, err = fileSHA256(filepath.Join(root, "usr/sbin/remotegate-agent"))
	return backup, err
}

func (b updateBackup) restore(ctx context.Context, logPath string) error {
	actual, err := fileSHA256(b.Package)
	if err != nil || actual != b.SHA256 {
		return errors.New("旧插件备份校验失败，已保留备份供人工恢复")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// Stop the failed client before invoking opkg's lifecycle scripts. Bound
	// each process group so a stuck script cannot consume all recovery time.
	_ = runUpdateService(ctx, "/etc/init.d/remotegate", "stop")
	for attempt := 0; attempt < 2; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		command := exec.CommandContext(attemptCtx, "opkg", "--force-downgrade", "--force-reinstall", "install", b.Package)
		prepareUpdateInstaller(command)
		command.Stdout, command.Stderr = logFile, logFile
		err = command.Run()
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return errors.New("旧插件重新安装失败，请查看路由器更新日志")
	}
	actual, err = fileSHA256("/usr/sbin/remotegate-agent")
	if err != nil || actual != b.AgentSHA256 {
		return errors.New("恢复后的客户端与备份不一致")
	}
	return nil
}

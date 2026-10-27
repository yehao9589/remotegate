package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/local/remotegate/internal/update"
)

const maxLocalUpload = 15 << 20

type localUpdateView struct {
	updateView
	FileSHA256 string `json:"fileSHA256"`
	Format     string `json:"format"`
}

// Parse archives without extracting paths or executing the uploaded shell header.
func localTar(raw []byte, visit func(string, *tar.Header, io.Reader) error) error {
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return errors.New("安装包不是有效的 gzip 归档")
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: 128<<20 + 1}
	tr := tar.NewReader(limited)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("安装包归档不完整")
		}
		if count >= 512 || h.Size < 0 || h.Size > 128<<20 {
			return errors.New("安装包内容超过限制")
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "." || name == "" {
			if h.Typeflag != tar.TypeDir {
				return errors.New("安装包路径无效")
			}
			continue
		}
		if path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || len(name) > 512 || seen[name] {
			return errors.New("安装包含异常或重复路径")
		}
		seen[name] = true
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return errors.New("安装包不允许链接或特殊文件")
		}
		if h.Mode&07000 != 0 {
			return errors.New("安装包包含异常文件权限")
		}
		if err = visit(name, h, tr); err != nil {
			return err
		}
	}
	if _, err = io.Copy(io.Discard, limited); err != nil {
		return errors.New("安装包压缩校验失败")
	}
	if limited.N == 0 {
		return errors.New("安装包解压大小超过限制")
	}
	return nil
}

func localMember(r io.Reader, size, limit int64) ([]byte, error) {
	if size > limit {
		return nil, errors.New("安装包文件超过大小限制")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(data)) != size {
		return nil, errors.New("安装包文件不完整")
	}
	return data, nil
}

func localIPKFromUpload(raw []byte, arch string) ([]byte, string, error) {
	if !bytes.HasPrefix(raw, []byte("#!/bin/sh\n")) {
		return raw, "ipk", nil
	}
	marker := []byte("\n__PAYLOAD__\n")
	offset := bytes.Index(raw, marker)
	if offset < 0 || offset > 16384 {
		return nil, "", errors.New("无法识别 .run 安装器")
	}
	var chosen []byte
	err := localTar(raw[offset+len(marker):], func(name string, h *tar.Header, r io.Reader) error {
		if h.Typeflag == tar.TypeDir {
			return errors.New("安装器内容无效")
		}
		if name != "remotegate-amd64.ipk" && name != "remotegate-arm64.ipk" && name != "remotegate-armv7.ipk" {
			return errors.New("安装器包含未知文件")
		}
		if name == "remotegate-"+arch+".ipk" {
			var err error
			chosen, err = localMember(r, h.Size, maxLocalUpload)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if chosen == nil {
		return nil, "", errors.New("安装器未包含当前路由器架构")
	}
	return chosen, "run", nil
}

func inspectLocalPackage(name, architecture string) (localUpdateView, []byte, error) {
	var view localUpdateView
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxLocalUpload {
		return view, nil, errors.New("请选择不超过 15 MiB 的 RemoteGate .ipk 或 .run 文件")
	}
	f, err := os.Open(name)
	if err != nil {
		return view, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxLocalUpload+1))
	f.Close()
	if err != nil || len(raw) > maxLocalUpload || int64(len(raw)) != info.Size() {
		return view, nil, errors.New("上传文件不完整")
	}
	arch := update.Architecture(architecture)
	if arch == "" {
		return view, nil, errors.New("当前 CPU 架构不受支持")
	}
	sourceHash := sha256.Sum256(raw)
	view.FileSHA256 = hex.EncodeToString(sourceHash[:])
	payload, format, err := localIPKFromUpload(raw, arch)
	if err != nil {
		return view, nil, err
	}
	view.Format = format
	members := map[string][]byte{}
	err = localTar(payload, func(name string, h *tar.Header, r io.Reader) error {
		if h.Typeflag == tar.TypeDir {
			return errors.New("IPK 外层内容无效")
		}
		limit := int64(maxLocalUpload)
		switch name {
		case "debian-binary":
			limit = 16
		case "control.tar.gz":
			limit = 1 << 20
		case "data.tar.gz":
		default:
			return errors.New("IPK 包含未知文件")
		}
		data, err := localMember(r, h.Size, limit)
		members[name] = data
		return err
	})
	if err != nil {
		return view, nil, err
	}
	if string(members["debian-binary"]) != "2.0\n" || len(members) != 3 {
		return view, nil, errors.New("不是受支持的 RemoteGate IPK 安装包")
	}
	fields := map[string]string{}
	err = localTar(members["control.tar.gz"], func(name string, h *tar.Header, r io.Reader) error {
		if h.Typeflag == tar.TypeDir {
			return errors.New("插件控制信息无效")
		}
		switch name {
		case "control", "conffiles", "preinst", "postinst", "prerm", "postrm":
		default:
			return errors.New("插件控制信息包含未知文件")
		}
		data, err := localMember(r, h.Size, 128<<10)
		if err != nil {
			return err
		}
		if name != "control" {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || (key != "Package" && key != "Version") {
				continue
			}
			if _, ok := fields[key]; ok {
				return errors.New("插件版本信息重复")
			}
			fields[key] = strings.TrimSpace(value)
		}
		return nil
	})
	if err != nil {
		return view, nil, err
	}
	hash := sha256.Sum256(payload)
	m := update.Manifest{PackageName: fields["Package"], PackageVersion: fields["Version"], AgentVersion: strings.Split(fields["Version"], "-")[0], SHA256: hex.EncodeToString(hash[:]), Size: int64(len(payload)), Architectures: []string{update.ArchitectureLabel(arch)}}
	if m.Validate() != nil {
		return view, nil, errors.New("仅支持有效的 luci-app-remotegate 插件包")
	}
	seen := map[string]bool{}
	err = localTar(members["data.tar.gz"], func(name string, h *tar.Header, r io.Reader) error {
		if h.Typeflag == tar.TypeDir {
			allowed := pluginFile("/" + name + "/.directory")
			for _, file := range []string{"etc/config/remotegate", "etc/init.d/remotegate", "usr/sbin/remotegate-agent", "usr/libexec/remotegate-config", "usr/share/remotegate/version", "usr/lib/lua/luci/controller/remotegate.lua", "usr/lib/lua/luci/model/cbi/remotegate.lua", "usr/lib/lua/luci/view/remotegate/map.htm"} {
				if strings.HasPrefix(file, name+"/") {
					allowed = true
				}
			}
			if !allowed {
				return errors.New("插件包含无关目录")
			}
			return nil
		}
		if !pluginFile("/" + name) {
			return errors.New("插件包含非 RemoteGate 文件")
		}
		seen[name] = true
		if name == "usr/share/remotegate/version" {
			data, err := localMember(r, h.Size, 128)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(data)) != m.PackageVersion {
				return errors.New("插件版本文件与安装信息不一致")
			}
		}
		if name == "usr/sbin/remotegate-agent" {
			var head [20]byte
			if _, err := io.ReadFull(r, head[:]); err != nil {
				return errors.New("客户端文件无效")
			}
			class, machine := byte(2), uint16(62)
			if arch == "arm64" {
				machine = 183
			}
			if arch == "armv7" {
				class, machine = 1, 40
			}
			if !bytes.Equal(head[:4], []byte{0x7f, 'E', 'L', 'F'}) || head[4] != class || head[5] != 1 || binary.LittleEndian.Uint16(head[18:]) != machine {
				return errors.New("安装包的实际 CPU 架构与当前路由器不匹配")
			}
		}
		return nil
	})
	if err != nil {
		return view, nil, err
	}
	for _, file := range []string{"etc/config/remotegate", "etc/init.d/remotegate", "usr/sbin/remotegate-agent", "usr/share/remotegate/version", "usr/libexec/remotegate-config", "usr/lib/lua/luci/controller/remotegate.lua", "usr/lib/lua/luci/view/remotegate/map.htm", "usr/lib/lua/luci/view/remotegate/style.htm", "usr/lib/lua/luci/view/remotegate/script.htm"} {
		if !seen[file] {
			return view, nil, errors.New("插件包缺少必要文件")
		}
	}
	view.RecoverySupported = true
	view.Manifest = &m
	return view, payload, nil
}

func checkLocalUpdate(name string, paths updatePaths) (localUpdateView, []byte, error) {
	view, payload, err := inspectLocalPackage(name, runtime.GOARCH)
	if err != nil {
		return view, nil, err
	}
	view.InstalledVersion = installedPackage(paths)
	view.State = readUpdateState(paths)
	comparison, err := update.Compare(view.Manifest.PackageVersion, view.InstalledVersion)
	if err != nil {
		return view, nil, err
	}
	if comparison <= 0 {
		return view, nil, errors.New("请选择比当前插件更新的版本；手动更新不支持降级或重复安装")
	}
	view.Available = true
	return view, payload, nil
}

func startLocalAgentUpdate(ctx context.Context, cfg Config, version, digest string) (updateView, error) {
	view, payload, err := checkLocalUpdate(filepath.Join(routerUpdatePaths.StateDir, "upload.bin"), routerUpdatePaths)
	if err != nil {
		return view.updateView, err
	}
	if view.Manifest.PackageVersion != version || view.FileSHA256 != digest {
		return view.updateView, errors.New("上传文件已变化，请重新上传并校验")
	}
	return launchAgentUpdate(ctx, cfg, *view.Manifest, payload)
}

func localUpdateCLI(args []string) {
	var value any
	var err error
	if args[0] == "--update-local-check" && len(args) == 2 {
		value, _, err = checkLocalUpdate(args[1], routerUpdatePaths)
	} else if args[0] == "--update-local-start" && len(args) == 4 {
		var cfg Config
		raw, e := os.ReadFile(args[1])
		if e != nil || len(raw) > 32768 || json.Unmarshal(raw, &cfg) != nil {
			err = errors.New("无法读取已保存的连接配置")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			value, err = startLocalAgentUpdate(ctx, cfg, args[2], args[3])
		}
	} else {
		err = errors.New("手动更新参数无效")
	}
	if err != nil {
		value = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

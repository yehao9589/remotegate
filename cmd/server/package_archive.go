package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/local/remotegate/internal/update"
)

func verifiedPackage(path string) (update.Manifest, error) {
	var m update.Manifest
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > update.MaxPackageSize {
		return m, errors.New("安装包文件不可用")
	}
	metadata, err := os.Lstat(path + ".json")
	if err != nil || !metadata.Mode().IsRegular() || metadata.Size() > 16384 {
		return m, errors.New("安装包清单不可用")
	}
	raw, err := os.ReadFile(path + ".json")
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &m) != nil || m.Validate() != nil || m.Size != info.Size() {
		return m, errors.New("安装包清单不可用")
	}
	if err = verifyArtifact(packageArtifact{Manifest: m, Path: path}); err != nil {
		return m, err
	}
	m.Artifact, m.Grant, m.Rollback = "", "", false
	return m, nil
}
func verifyArtifact(item packageArtifact) error {
	info, err := os.Lstat(item.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Manifest.Size {
		return errors.New("安装包文件已变化")
	}
	f, err := os.Open(item.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(f, update.MaxPackageSize+1)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != item.Manifest.SHA256 {
		return errors.New("安装包校验失败")
	}
	return nil
}
func (a *app) archivePackages(p *packageCenter) error {
	current, err := a.loadUpdatePackage()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.catalogHash == current.SHA256 {
		return nil
	}
	if err = os.MkdirAll(p.directory, 0700); err != nil {
		return err
	}
	// Archive only verified packages supplied alongside the installed server.
	paths := []string{installerPath()}
	neighbors, _ := filepath.Glob(filepath.Join(filepath.Dir(installerPath()), "RemoteGate-*-istore.run"))
	if len(neighbors) <= 32 {
		paths = append(paths, neighbors...)
	}
	for _, path := range paths {
		m, err := verifiedPackage(path)
		if err != nil || !compatiblePackage(m) {
			continue
		}
		destination := filepath.Join(p.directory, m.SHA256+".run")
		item := packageArtifact{Manifest: m, Path: destination, Current: m.SHA256 == current.SHA256}
		if verifyArtifact(item) != nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err = writePackageBytes(destination, data); err != nil {
				return err
			}
			if err = writePackageFile(destination+".json", m); err != nil {
				return err
			}
		}
	}
	archives, _ := filepath.Glob(filepath.Join(p.directory, "*.run.json"))
	if len(archives) > 128 {
		return errors.New("历史安装包数量超出限制")
	}
	for _, metadata := range archives {
		path := strings.TrimSuffix(metadata, ".json")
		if len(strings.TrimSuffix(filepath.Base(path), ".run")) != 64 {
			continue
		}
		m, err := verifiedPackage(path)
		if err != nil {
			continue
		}
		p.artifacts[m.SHA256] = packageArtifact{Manifest: m, Path: path, Current: m.SHA256 == current.SHA256}
	}
	p.pruneArchivesLocked()
	p.catalogHash = current.SHA256
	return nil
}

func (p *packageCenter) pruneArchivesLocked() {
	var items []packageArtifact
	for _, item := range p.artifacts {
		if item.Arch == "" {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		n, _ := update.Compare(items[i].Manifest.PackageVersion, items[j].Manifest.PackageVersion)
		return n > 0
	})
	protected := map[string]bool{}
	for _, grant := range p.grants {
		if time.Now().Before(grant.Expires) {
			protected[strings.TrimSuffix(strings.SplitN(filepath.Base(grant.Artifact.Path), "-", 2)[0], ".run")] = true
		}
	}
	if p.state.Batch != nil && (p.state.Batch.Phase == "running" || p.state.Batch.Phase == "queued" || p.state.Batch.Phase == "paused") {
		protected[p.state.Batch.SHA256] = true
	}
	for i, item := range items {
		if i < 5 || protected[item.Manifest.SHA256] {
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(p.directory, item.Manifest.SHA256+"*.run*"))
		for _, path := range paths {
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
				_ = os.Remove(path)
			}
		}
		for hash, cached := range p.artifacts {
			if cached.Path == item.Path || strings.HasPrefix(filepath.Base(cached.Path), item.Manifest.SHA256+"-") {
				delete(p.artifacts, hash)
			}
		}
	}
}
func (a *app) packageArtifact(p *packageCenter, version, arch string) (packageArtifact, error) {
	if err := a.archivePackages(p); err != nil {
		return packageArtifact{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var base packageArtifact
	for _, item := range p.artifacts {
		if item.Arch == "" && ((version == "" && item.Current) || (version != "" && item.Manifest.PackageVersion == version)) {
			base = item
			break
		}
	}
	if base.Path == "" || !compatiblePackage(base.Manifest) {
		return base, errors.New("服务器没有兼容的历史安装包")
	}
	if arch == "" {
		return base, verifyArtifact(base)
	}
	arch = update.Architecture(arch)
	if arch == "" {
		return base, errors.New("不支持此 CPU 架构")
	}
	cachePath := filepath.Join(p.directory, base.Manifest.SHA256+"-"+arch+".run")
	for _, item := range p.artifacts {
		if item.Path == cachePath {
			return item, verifyArtifact(item)
		}
	}
	data, err := update.CompactPackage(base.Path, arch)
	if err != nil {
		return base, err
	}
	sum := sha256.Sum256(data)
	m := base.Manifest
	m.Size, m.SHA256, m.Architectures = int64(len(data)), hex.EncodeToString(sum[:]), []string{update.ArchitectureLabel(arch)}
	if err = writePackageBytes(cachePath, data); err != nil {
		return base, err
	}
	item := packageArtifact{Manifest: m, Path: cachePath, Arch: arch, Current: base.Current}
	p.artifacts[m.SHA256] = item
	return item, nil
}
func (a *app) packageCatalog(p *packageCenter) ([]map[string]any, []map[string]any, string) {
	base, err := a.packageArtifact(p, "", "")
	if err != nil {
		return nil, nil, err.Error()
	}
	var catalog []map[string]any
	p.mu.Lock()
	for _, item := range p.artifacts {
		if item.Arch == "" {
			catalog = append(catalog, map[string]any{"version": item.Manifest.PackageVersion, "agentVersion": item.Manifest.AgentVersion, "size": item.Manifest.Size, "current": item.Current, "compatible": compatiblePackage(item.Manifest)})
		}
	}
	p.mu.Unlock()
	sort.Slice(catalog, func(i, j int) bool {
		n, _ := update.Compare(catalog[i]["version"].(string), catalog[j]["version"].(string))
		return n > 0
	})
	if len(catalog) > 5 {
		catalog = catalog[:5]
	}
	downloads := []map[string]any{{"arch": "", "label": "通用安装包", "size": base.Manifest.Size, "sha256": base.Manifest.SHA256, "url": "/downloads/remotegate.run"}}
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		item, err := a.packageArtifact(p, "", arch)
		if err == nil {
			download := map[string]any{"arch": arch, "label": update.ArchitectureLabel(arch), "size": item.Manifest.Size, "sha256": item.Manifest.SHA256, "url": "/downloads/remotegate.run?arch=" + arch}
			if _, ipk, err := packageIPK(base, arch); err == nil {
				download["ipk"] = ipk
			}
			downloads = append(downloads, download)
		}
	}
	return catalog, downloads, ""
}
func (a *app) issuePackageGrant(p *packageCenter, device string, item packageArtifact, rollback bool) (update.Manifest, error) {
	key := packageSecret()
	if key == "" {
		return update.Manifest{}, errors.New("更新授权生成失败")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, grant := range p.grants {
		if time.Now().After(grant.Expires) {
			delete(p.grants, key)
		}
	}
	if len(p.grants) >= 256 {
		return update.Manifest{}, errors.New("更新任务数量已达上限")
	}
	p.grants[key] = packageGrant{device, item, rollback, time.Now().Add(15 * time.Minute)}
	m := item.Manifest
	m.Artifact, m.Grant, m.Rollback = m.SHA256, key, rollback
	return m, nil
}
func (a *app) grantedArtifact(p *packageCenter, device, artifact, key string) (packageArtifact, bool, error) {
	p.mu.Lock()
	grant, exists := p.grants[key]
	p.mu.Unlock()
	if !exists || grant.Device != device || grant.Artifact.Manifest.SHA256 != artifact || time.Now().After(grant.Expires) {
		return packageArtifact{}, false, errors.New("更新授权已过期或不属于当前设备")
	}
	return grant.Artifact, grant.Rollback, verifyArtifact(grant.Artifact)
}

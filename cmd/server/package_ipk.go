package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/local/remotegate/internal/update"
)

// IPKs are extracted in memory from the verified installer used by updates.
func packageIPK(base packageArtifact, arch string) ([]byte, map[string]any, error) {
	_, payload, err := update.PackagePayload(base.Path, arch)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(payload)
	query := url.Values{"arch": {arch}, "version": {base.Manifest.PackageVersion}}
	return payload, map[string]any{"size": len(payload), "sha256": hex.EncodeToString(sum[:]), "url": "/downloads/remotegate.ipk?" + query.Encode()}, nil
}

func (a *app) ipkDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	arch := update.Architecture(r.URL.Query().Get("arch"))
	if arch == "" {
		http.Error(w, "请先选择路由器固件的 CPU 架构", http.StatusBadRequest)
		return
	}
	p, err := a.packageCenterStore()
	if err != nil {
		http.Error(w, "安装包不可用", http.StatusServiceUnavailable)
		return
	}
	base, err := a.packageArtifact(p, r.URL.Query().Get("version"), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	payload, metadata, err := packageIPK(base, arch)
	if err != nil {
		http.Error(w, "此安装包未提供所选架构", http.StatusServiceUnavailable)
		return
	}
	digest := metadata["sha256"].(string)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "RemoteGate-" + base.Manifest.PackageVersion + "-" + arch + ".ipk"}))
	w.Header().Set("X-Checksum-SHA256", digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "remotegate.ipk", time.Time{}, bytes.NewReader(payload))
}

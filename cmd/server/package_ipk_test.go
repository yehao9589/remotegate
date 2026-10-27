package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/local/remotegate/internal/update"
)

func TestNativeIPKDownloadPreservesArchitecturePayloadAndMetadata(t *testing.T) {
	a, _, _, cookie := centerFixture(t)
	p, _ := a.packageCenterStore()
	base, err := a.packageArtifact(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	catalog := packageCall(a, cookie, "GET", "/api/admin/package-center", nil)
	requireStatus(t, catalog, 200)
	var view struct {
		Downloads []struct {
			Arch string `json:"arch"`
			IPK  struct {
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
				Size   int    `json:"size"`
			} `json:"ipk"`
		} `json:"downloads"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, item := range view.Downloads {
		if item.Arch == "" {
			continue
		}
		seen++
		_, expected, err := update.PackagePayload(base.Path, item.Arch)
		if err != nil {
			t.Fatal(err)
		}
		w := packageCall(a, nil, "GET", item.IPK.URL, nil)
		requireStatus(t, w, 200)
		if !bytes.Equal(w.Body.Bytes(), expected) {
			t.Fatal("IPK differs from verified installer", item.Arch)
		}
		sum := sha256.Sum256(expected)
		digest := hex.EncodeToString(sum[:])
		if item.IPK.SHA256 != digest || item.IPK.Size != len(expected) || w.Header().Get("X-Checksum-SHA256") != digest {
			t.Fatal("IPK metadata does not match body", item.Arch)
		}
		kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if err != nil || kind != "attachment" || params["filename"] != "RemoteGate-"+base.Manifest.PackageVersion+"-"+item.Arch+".ipk" {
			t.Fatal("Incorrect IPK filename", w.Header())
		}
		head := packageCall(a, nil, "HEAD", item.IPK.URL, nil)
		requireStatus(t, head, 200)
		if head.Body.Len() != 0 || head.Header().Get("Content-Length") != w.Header().Get("Content-Length") {
			t.Fatal("HEAD changed size or included body")
		}
		req := httptest.NewRequest("GET", item.IPK.URL, nil)
		req.Header.Set("Range", "bytes=0-9")
		partial := httptest.NewRecorder()
		a.routes().ServeHTTP(partial, req)
		requireStatus(t, partial, 206)
		if !bytes.Equal(partial.Body.Bytes(), expected[:10]) {
			t.Fatal("Range download differs")
		}
		req = httptest.NewRequest("GET", item.IPK.URL, nil)
		req.Header.Set("If-None-Match", w.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		a.routes().ServeHTTP(cached, req)
		requireStatus(t, cached, 304)
	}
	if seen != 3 {
		t.Fatal("Expected three explicit architectures", seen)
	}
	legacy := packageCall(a, nil, "GET", "/downloads/remotegate.run", nil)
	requireStatus(t, legacy, 200)
	if !strings.HasPrefix(legacy.Body.String(), "#!/bin/sh\n") {
		t.Fatal("iStore installer changed")
	}
	alias := packageCall(a, nil, "GET", "/downloads/remotegate.ipk?arch=aarch64", nil)
	requireStatus(t, alias, 200)
	_, expected, _ := update.PackagePayload(base.Path, "arm64")
	if !bytes.Equal(alias.Body.Bytes(), expected) {
		t.Fatal("Architecture alias changed payload")
	}
}

func TestNativeIPKDownloadRejectsInvalidArchitectureMethodAndTampering(t *testing.T) {
	a, _, _, _ := centerFixture(t)
	for _, arch := range []string{"", "all", "mips", "../../secret"} {
		requireStatus(t, packageCall(a, nil, "GET", "/downloads/remotegate.ipk?arch="+arch, nil), 400)
	}
	requireStatus(t, packageCall(a, nil, "POST", "/downloads/remotegate.ipk?arch=arm64", nil), 405)
	requireStatus(t, packageCall(a, nil, "GET", "/downloads/remotegate.ipk?arch=arm64&version=99.0.0-1", nil), 503)
	p, _ := a.packageCenterStore()
	base, err := a.packageArtifact(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := packageCall(a, nil, "GET", "/downloads/remotegate.ipk?arch=arm64", nil)
	requireStatus(t, failed, 503)
	if strings.Contains(failed.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("Invalid package offered for download")
	}
}

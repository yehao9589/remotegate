package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/remotegate/internal/update"
)

type localTestFile struct {
	name string
	data []byte
	kind byte
}

func localTestTar(t *testing.T, files []localTestFile) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	w := tar.NewWriter(z)
	for _, f := range files {
		kind := f.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(f.data))
		if kind != tar.TypeReg {
			size = 0
		}
		h := &tar.Header{Name: f.name, Mode: 0644, Size: size, Typeflag: kind}
		if kind == tar.TypeSymlink {
			h.Linkname = "/etc/passwd"
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := w.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func localTestIPK(t *testing.T, arch, version, problem string) []byte {
	t.Helper()
	head := make([]byte, 20)
	copy(head, []byte{0x7f, 'E', 'L', 'F'})
	head[4], head[5] = 2, 1
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	if arch == "armv7" {
		head[4], machine = 1, 40
	}
	binary.LittleEndian.PutUint16(head[18:], machine)
	files := []localTestFile{}
	for _, name := range []string{"etc/config/remotegate", "etc/init.d/remotegate", "usr/libexec/remotegate-config", "usr/lib/lua/luci/controller/remotegate.lua", "usr/lib/lua/luci/view/remotegate/map.htm", "usr/lib/lua/luci/view/remotegate/style.htm", "usr/lib/lua/luci/view/remotegate/script.htm"} {
		files = append(files, localTestFile{name, []byte("fixture"), 0})
	}
	files = append(files, localTestFile{"usr/sbin/remotegate-agent", head, 0}, localTestFile{"usr/share/remotegate/version", []byte(version + "\n"), 0})
	if problem == "path" {
		files = append(files, localTestFile{"../../etc/passwd", []byte("bad"), 0})
	}
	if problem == "foreign" {
		files = append(files, localTestFile{"etc/shadow", []byte("bad"), 0})
	}
	if problem == "link" {
		files = append(files, localTestFile{"usr/share/remotegate/link", nil, tar.TypeSymlink})
	}
	if problem == "duplicate" {
		files = append(files, files[0])
	}
	if problem == "version" {
		files[len(files)-1].data = []byte("1.0.0-1")
	}
	name := "luci-app-remotegate"
	if problem == "package" {
		name = "other-plugin"
	}
	control := []byte("Package: " + name + "\nVersion: " + version + "\nArchitecture: all\n")
	metadata := localTestTar(t, []localTestFile{{"control", control, 0}, {"conffiles", []byte("/etc/config/remotegate\n"), 0}})
	return localTestTar(t, []localTestFile{{"debian-binary", []byte("2.0\n"), 0}, {"control.tar.gz", metadata, 0}, {"data.tar.gz", localTestTar(t, files), 0}})
}
func writeLocalTestFile(t *testing.T, raw []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLocalIPKAndRunSelectActualArchitectureWithoutExecutingHeader(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		ipk := localTestIPK(t, arch, "9.0.0-1", "")
		name := writeLocalTestFile(t, ipk)
		view, body, err := inspectLocalPackage(name, arch)
		if err != nil || !bytes.Equal(body, ipk) || view.Manifest.PackageVersion != "9.0.0-1" || view.Format != "ipk" {
			t.Fatal(arch, view, err)
		}
		packed := localTestTar(t, []localTestFile{{"remotegate-" + arch + ".ipk", ipk, 0}})
		run := append([]byte("#!/bin/sh\necho 'THIS MUST NEVER RUN'\nexit 99\n__PAYLOAD__\n"), packed...)
		runView, selected, err := inspectLocalPackage(writeLocalTestFile(t, run), arch)
		if err != nil || runView.Format != "run" || !bytes.Equal(selected, ipk) || runView.Manifest.SHA256 != view.Manifest.SHA256 || runView.FileSHA256 == view.FileSHA256 {
			t.Fatal(arch, runView, err)
		}
		other := "amd64"
		if arch == other {
			other = "arm64"
		}
		if _, _, err := inspectLocalPackage(name, other); err == nil {
			t.Fatal("Wrong ELF architecture accepted")
		}
		if _, _, err := inspectLocalPackage(writeLocalTestFile(t, run), other); err == nil {
			t.Fatal("Run package without native architecture accepted")
		}
	}
}
func TestLocalPackageRejectsOtherPackagesCorruptionPathsLinksAndVersions(t *testing.T) {
	for _, problem := range []string{"path", "foreign", "link", "duplicate", "version", "package"} {
		t.Run(problem, func(t *testing.T) {
			if _, _, err := inspectLocalPackage(writeLocalTestFile(t, localTestIPK(t, "amd64", "9.0.0-1", problem)), "amd64"); err == nil {
				t.Fatal("Unsafe package accepted")
			}
		})
	}
	corrupt := localTestIPK(t, "amd64", "9.0.0-1", "")
	corrupt[len(corrupt)-5] ^= 1
	if _, _, err := inspectLocalPackage(writeLocalTestFile(t, corrupt), "amd64"); err == nil {
		t.Fatal("Broken gzip checksum accepted")
	}
	if _, _, err := inspectLocalPackage(writeLocalTestFile(t, []byte("not a package")), "amd64"); err == nil {
		t.Fatal("Garbage accepted")
	}
	oversized := writeLocalTestFile(t, []byte("x"))
	if err := os.Truncate(oversized, maxLocalUpload+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectLocalPackage(oversized, "amd64"); err == nil {
		t.Fatal("Oversized upload accepted")
	}
	paths := updatePaths{StateDir: t.TempDir(), Version: filepath.Join(t.TempDir(), "version")}
	os.WriteFile(paths.Version, []byte("9.0.0-1"), 0600)
	for _, version := range []string{"9.0.0-1", "8.0.0-1"} {
		if _, _, err := checkLocalUpdate(writeLocalTestFile(t, localTestIPK(t, update.Architecture(runtime.GOARCH), version, "")), paths); err == nil {
			t.Fatal("Same or old version accepted")
		}
	}
}
func TestLocalUpdateUsesNoDownloadAndRestoresConfigurationOnFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "must not download", 500) }))
	defer server.Close()
	for _, failure := range []string{"", "install", "heartbeat", "hash"} {
		t.Run(failure, func(t *testing.T) {
			raw := localTestIPK(t, update.Architecture(runtime.GOARCH), "9.0.0-1", "")
			view, _, err := inspectLocalPackage(writeLocalTestFile(t, raw), runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			paths := updatePaths{StateDir: filepath.Join(dir, "status"), Configuration: filepath.Join(dir, "uci"), Version: filepath.Join(dir, "version")}
			original := []byte("same device, token, server, interface and disabled flag")
			os.WriteFile(paths.Configuration, original, 0600)
			os.WriteFile(paths.Version, []byte("0.1.18-2"), 0600)
			os.WriteFile(filepath.Join(dir, "uploaded.ipk"), raw, 0600)
			manifest := *view.Manifest
			if failure == "hash" {
				manifest.SHA256 = strings.Repeat("0", 64)
			}
			backups, installs, restores := 0, 0, 0
			recovery := updateRecovery{Backup: func() error { backups++; return nil }, Restore: func(context.Context) error { restores++; return os.WriteFile(paths.Version, []byte("0.1.18-2"), 0600) }, Verify: func(context.Context, time.Time) error {
				if failure == "heartbeat" && installedPackage(paths) == manifest.PackageVersion {
					return errors.New("no heartbeat")
				}
				return nil
			}}
			err = performUpdate(context.Background(), updatePlan{Config: Config{ServerURL: server.URL, DeviceID: "same", Token: "secret"}, Manifest: manifest, State: update.Status{StartedAt: time.Now()}, LocalPackage: true}, dir, paths, func(_ context.Context, name string) error {
				installs++
				if !strings.HasSuffix(name, "uploaded.ipk") {
					t.Fatal("Upload sent to shell installer")
				}
				os.WriteFile(paths.Configuration, []byte("overwritten"), 0600)
				os.WriteFile(paths.Version, []byte(manifest.PackageVersion), 0600)
				if failure == "install" {
					return errors.New("install failure")
				}
				return nil
			}, func(context.Context, bool) error {
				data, _ := os.ReadFile(paths.Configuration)
				if !bytes.Equal(data, original) {
					t.Fatal("Identity not restored before restart")
				}
				return nil
			}, false, recovery)
			if (err != nil) != (failure != "") {
				t.Fatal(failure, err)
			}
			if failure == "hash" {
				if backups != 0 || installs != 0 || restores != 0 {
					t.Fatal("Corrupt upload mutated plugin")
				}
			} else {
				if backups != 1 || installs != 1 {
					t.Fatal(backups, installs)
				}
				if failure != "" && (!readUpdateState(paths).RolledBack || restores != 1) {
					t.Fatal("Local update failed to roll back")
				}
			}
			data, _ := os.ReadFile(paths.Configuration)
			if !bytes.Equal(data, original) {
				t.Fatal("Saved configuration changed")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("Manual update attempted a server download")
	}
}

//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedPluginFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"/etc/config/remotegate":                       "original secret identity",
		"/etc/init.d/remotegate":                       "#!/bin/sh\nold init\n",
		"/usr/sbin/remotegate-agent":                   "old actual client",
		"/usr/share/remotegate/version":                "0.1.15-1\n",
		"/usr/libexec/remotegate-config":               "old config generator",
		"/usr/lib/lua/luci/view/remotegate/script.htm": "old UI",
	}
	var names []string
	for name, content := range files {
		full := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(full), 0755)
		if err := os.WriteFile(full, []byte(content), 0750); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	prefix := filepath.Join(root, "usr/lib/opkg/info/luci-app-remotegate")
	_ = os.MkdirAll(filepath.Dir(prefix), 0755)
	for suffix, content := range map[string]string{"control": "Package: luci-app-remotegate\nVersion: 0.1.15-1\nArchitecture: all\n", "postinst": "#!/bin/sh\nexit 0\n", "conffiles": "/etc/config/remotegate\n", "list": strings.Join(names, "\n") + "\n"} {
		if err := os.WriteFile(prefix+"."+suffix, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root, files
}

func unpackTestArchive(t *testing.T, raw []byte) (map[string][]byte, map[string]int64) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			t.Fatal(h)
		}
		files[h.Name], err = io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		modes[h.Name] = h.Mode
	}
	return files, modes
}

func TestBackupContainsInstalledFilesAndOpkgMetadata(t *testing.T) {
	root, expected := installedPluginFixture(t)
	b, err := backupInstalledPlugin(root, t.TempDir(), "0.1.15-1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(b.Package)
	if info.Mode().Perm() != 0600 {
		t.Fatal("recovery package is not private")
	}
	raw, _ := os.ReadFile(b.Package)
	outer, _ := unpackTestArchive(t, raw)
	if string(outer["debian-binary"]) != "2.0\n" {
		t.Fatal("invalid ipk envelope")
	}
	data, modes := unpackTestArchive(t, outer["data.tar.gz"])
	for name, content := range expected {
		if string(data[strings.TrimPrefix(name, "/")]) != content || modes[strings.TrimPrefix(name, "/")] != 0750 {
			t.Fatal("backup differs from installed file", name)
		}
	}
	control, _ := unpackTestArchive(t, outer["control.tar.gz"])
	if !strings.Contains(string(control["control"]), "Version: 0.1.15-1\n") || len(control["postinst"]) == 0 || string(control["conffiles"]) != "/etc/config/remotegate\n" {
		t.Fatal("missing package database/scripts")
	}
	hash, _ := fileSHA256(b.Package)
	agentHash, _ := fileSHA256(filepath.Join(root, "usr/sbin/remotegate-agent"))
	if b.SHA256 != hash || b.AgentSHA256 != agentHash {
		t.Fatal("invalid recovery digests")
	}
}

func TestBackupRejectsIncompleteOrUnsafeInstalledFiles(t *testing.T) {
	for _, bad := range []string{"missing", "traversal", "unrelated", "symlink", "parent-link", "version"} {
		t.Run(bad, func(t *testing.T) {
			root, _ := installedPluginFixture(t)
			prefix := filepath.Join(root, "usr/lib/opkg/info/luci-app-remotegate")
			name := filepath.Join(root, "usr/sbin/remotegate-agent")
			switch bad {
			case "missing":
				_ = os.Remove(name)
			case "traversal", "unrelated":
				f, _ := os.OpenFile(prefix+".list", os.O_APPEND|os.O_WRONLY, 0600)
				extra := "/etc/shadow\n"
				if bad == "traversal" {
					extra = "/usr/share/remotegate/../../other\n"
				}
				_, _ = f.WriteString(extra)
				_ = f.Close()
			case "symlink":
				_ = os.Remove(name)
				_ = os.Symlink("/etc/passwd", name)
			case "parent-link":
				_ = os.Rename(filepath.Dir(name), filepath.Join(root, "usr/moved"))
				_ = os.Symlink(filepath.Join(root, "usr/moved"), filepath.Dir(name))
			case "version":
				_ = os.WriteFile(prefix+".control", []byte("Package: luci-app-remotegate\nVersion: 0.1.15-9\n"), 0600)
			}
			if _, err := backupInstalledPlugin(root, t.TempDir(), "0.1.15-1", 1000); err == nil {
				t.Fatal("unsafe backup accepted")
			}
		})
	}
}

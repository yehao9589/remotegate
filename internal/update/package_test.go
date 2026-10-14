package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactPackagePreservesSelectedIPKAndRejectsMalformedPayload(t *testing.T) {
	var data bytes.Buffer
	data.WriteString("#!/bin/sh\nexit 0\n__PAYLOAD__\n")
	z := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(z)
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		payload := bytes.Repeat([]byte(arch+"-data"), 4096)
		_ = tarWriter.WriteHeader(&tar.Header{Name: "remotegate-" + arch + ".ipk", Size: int64(len(payload)), Mode: 0644, Typeflag: tar.TypeReg})
		_, _ = tarWriter.Write(payload)
	}
	_ = tarWriter.Close()
	_ = z.Close()
	path := filepath.Join(t.TempDir(), "common.run")
	_ = os.WriteFile(path, data.Bytes(), 0600)
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		compact, err := CompactPackage(path, arch)
		if err != nil {
			t.Fatal(err)
		}
		selected := filepath.Join(t.TempDir(), "selected.run")
		_ = os.WriteFile(selected, compact, 0600)
		_, payload, err := PackagePayload(selected, arch)
		if err != nil || !bytes.Equal(payload, bytes.Repeat([]byte(arch+"-data"), 4096)) {
			t.Fatal("selected bytes changed", err)
		}
		other := "arm64"
		if arch == other {
			other = "amd64"
		}
		if _, _, err = PackagePayload(selected, other); err == nil {
			t.Fatal("compact package included an unrelated architecture")
		}
	}
	if _, err := CompactPackage(path, "../../outside"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
	_ = os.WriteFile(path, []byte("#!/bin/sh\n__PAYLOAD__\ninvalid gzip"), 0600)
	if _, err := CompactPackage(path, "amd64"); err == nil {
		t.Fatal("malformed archive accepted")
	}
}

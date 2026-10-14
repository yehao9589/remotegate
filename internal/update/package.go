package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
)

func Architecture(value string) string {
	switch value {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64", "ARM64":
		return "arm64"
	case "arm", "armv7", "armv7l", "ARMv7":
		return "armv7"
	}
	return ""
}

func ArchitectureLabel(value string) string {
	return map[string]string{"amd64": "x86_64", "arm64": "ARM64", "armv7": "ARMv7"}[Architecture(value)]
}

// PackagePayload extracts only the named IPK from a verified RemoteGate installer.
// It never writes archive paths to the filesystem.
func PackagePayload(path, arch string) ([]byte, []byte, error) {
	arch = Architecture(arch)
	if arch == "" {
		return nil, nil, errors.New("不支持此 CPU 架构")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	header := make([]byte, 16384)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	marker := []byte("\n__PAYLOAD__\n")
	end := bytes.Index(header[:n], marker)
	if end < 0 || !bytes.HasPrefix(header, []byte("#!/bin/sh\n")) {
		return nil, nil, errors.New("安装包不支持按架构下载")
	}
	end += len(marker)
	if _, err = f.Seek(int64(end), io.SeekStart); err != nil {
		return nil, nil, err
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	defer z.Close()
	reader := tar.NewReader(io.LimitReader(z, 3*MaxPackageSize))
	var selected []byte
	for entries := 0; entries < 16; entries++ {
		h, err := reader.Next()
		if err == io.EOF {
			if selected == nil {
				return nil, nil, errors.New("安装包未包含此 CPU 架构")
			}
			return header[:end], selected, nil
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Size <= 0 || h.Size > MaxPackageSize || h.Typeflag != tar.TypeReg {
			return nil, nil, errors.New("安装包内容无效")
		}
		if h.Name == "remotegate-"+arch+".ipk" {
			if selected != nil {
				return nil, nil, errors.New("安装包架构内容重复")
			}
			selected, err = io.ReadAll(reader)
			if err != nil || int64(len(selected)) != h.Size {
				return nil, nil, errors.New("安装包内容不完整")
			}
		}
	}
	return nil, nil, errors.New("安装包内容过多")
}

func CompactPackage(path, arch string) ([]byte, error) {
	header, payload, err := PackagePayload(path, arch)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(header)
	z, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	t := tar.NewWriter(z)
	err = t.WriteHeader(&tar.Header{Name: "remotegate-" + Architecture(arch) + ".ipk", Size: int64(len(payload)), Mode: 0644, Typeflag: tar.TypeReg})
	if err == nil {
		_, err = t.Write(payload)
	}
	if err == nil {
		err = t.Close()
	}
	if closeErr := z.Close(); err == nil {
		err = closeErr
	}
	return out.Bytes(), err
}

package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func caName(s certificateState) string {
	if s.CA == "" {
		return "letsencrypt"
	}
	return s.CA
}
func caDirectory(s certificateState) string {
	if caName(s) == "zerossl" {
		return "https://acme.zerossl.com/v2/DV90"
	}
	return "https://acme-v02.api.letsencrypt.org/directory"
}
func requestedNames(s certificateState) []string {
	if len(s.Names) > 0 {
		return s.Names
	}
	if s.Domain == "" {
		return []string{}
	}
	return []string{s.Domain, "*." + s.Domain}
}

var certDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func normalizeCertificateNames(names []string, base string) ([]string, error) {
	if len(names) > 30 {
		return nil, errors.New("单张证书最多填写 30 个域名")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		host := strings.TrimPrefix(name, "*.")
		if len(host) > 253 || !certDomainPattern.MatchString(host) || (host != base && !strings.HasSuffix(host, "."+base)) {
			return nil, fmt.Errorf("域名 %s 必须属于 %s，且不含协议、端口或路径", name, base)
		}
		if !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	if len(out) == 0 {
		return nil, errors.New("至少选择一个证书域名")
	}
	return out, nil
}
func verifyRequestedNames(pair *tls.Certificate, names []string) error {
	for _, name := range names {
		if strings.HasPrefix(name, "*.") {
			found := false
			for _, n := range pair.Leaf.DNSNames {
				if strings.EqualFold(n, name) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("签发证书缺少 %s", name)
			}
		} else if err := pair.Leaf.VerifyHostname(name); err != nil {
			return fmt.Errorf("签发证书不覆盖 %s", name)
		}
	}
	return nil
}
func readCertificateFiles(certPath, keyPath string) (string, string, error) {
	read := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			return "", errors.New("请填写服务端上的绝对路径")
		}
		f, e := os.Open(path)
		if e != nil {
			return "", errors.New("无法读取证书文件，请检查路径和服务端权限")
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 {
			return "", errors.New("证书文件必须是小于 256 KiB 的普通文件")
		}
		b, e := io.ReadAll(io.LimitReader(f, 256<<10))
		return string(b), e
	}
	c, e := read(certPath)
	if e != nil {
		return "", "", e
	}
	k, e := read(keyPath)
	return c, k, e
}

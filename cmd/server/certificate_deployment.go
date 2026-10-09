package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func privateDeploymentTransport(r *http.Request) bool {
	if secureAuthRequest(r) {
		return true
	}
	// Permit local development, but do not mistake a public HTTP reverse proxy
	// for a direct local request just because its upstream peer is loopback.
	if !loopbackPeer(r) || (hostOnly(r.Host) != "localhost" && !netLoopbackHost(r.Host)) || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := parsePublicURL(origin)
		if err != nil || (u.Hostname() != "localhost" && !netLoopbackHost(u.Host)) {
			return false
		}
	}
	return true
}

func (a *app) exportCertificateDeployment(w http.ResponseWriter, r *http.Request, m *certificateManager, domain, password string) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameAuthOrigin(r) {
		http.Error(w, "请求来源不匹配", 403)
		return
	}
	if !privateDeploymentTransport(r) {
		http.Error(w, "部署包包含私钥，请通过 HTTPS 后台下载", 400)
		return
	}
	if a.auth == nil || !a.auth.initialized() {
		http.Error(w, "管理员认证未初始化", 503)
		return
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	a.auth.mu.Lock()
	limit := a.auth.limits[peer]
	a.auth.mu.Unlock()
	if limit.failures >= 5 && time.Now().Before(limit.until) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "尝试次数过多，请一分钟后再试", 429)
		return
	}
	user := a.auth.securityInfo()["username"].(string)
	if !a.auth.verify(user, password) {
		a.auth.mu.Lock()
		limit = a.auth.limits[peer]
		if time.Now().After(limit.until) {
			limit = loginLimit{until: time.Now().Add(time.Minute)}
		}
		limit.failures++
		a.auth.limits[peer] = limit
		a.auth.mu.Unlock()
		http.Error(w, "管理员密码不正确", 400)
		return
	}
	m.mu.Lock()
	s := m.state
	installed := m.pair != nil
	m.mu.Unlock()
	if !installed {
		http.Error(w, "尚未安装证书", 400)
		return
	}
	_, leaf, err := validateCertificate(s.CertPEM, s.KeyPEM, domain)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	rootNote := "证书覆盖主域名，可在绑定这些名称的站点统一部署。"
	if leaf.VerifyHostname(domain) != nil {
		rootNote = "此证书不覆盖主域名 " + domain + "。请给子域名单独创建站点，不要覆盖现有主域名站点的证书。"
	}
	readme := fmt.Sprintf(`# HTTPS 证书部署包

覆盖域名：%s
到期时间：%s

## 宝塔部署

1. 在对应子域名站点的域名管理中绑定要访问的名称。
2. SSL → 当前证书：将 fullchain.pem 内容填入证书 PEM，将 privkey.pem 内容填入密钥 KEY；保存并启用。
3. 反向代理到 http://127.0.0.1:18088，并使用 proxy-headers.conf 中的转发头。替换已有 Host 行，不重复添加。
4. 在 RemoteGate 里将映射公网协议与端口填写为 HTTPS / 443（如代理用其他端口，填写真实端口）。
5. 在“解析接入”检测具体映射域名和端口，确认公网证书验证通过，再检测设备内网服务。

%s

## 续期

RemoteGate 续期成功会自动更新它自己的 HTTPS 入口。这个下载包是一份当前快照；宝塔不会自动读取它。续期后请重新下载并导入。若希望证书续期直接生效，可在后台启用内置 HTTPS 并让映射使用该端口。

本包包含私钥，只用于你自己的 HTTPS 入口。没有 DNS 凭据、管理员密码或设备令牌。
`, strings.Join(leaf.DNSNames, "、"), leaf.NotAfter.UTC().Format(time.RFC3339), rootNote)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, file := range []struct{ name, content string }{{"fullchain.pem", s.CertPEM}, {"privkey.pem", s.KeyPEM}, {"README.md", readme}, {"proxy-headers.conf", "# 在现有代理 location 中替换对应转发头；其余 WebSocket 配置保留。\nproxy_set_header Host $http_host;\nproxy_set_header X-Forwarded-Proto $scheme;\n"}} {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(0600)
		writer, e := archive.CreateHeader(header)
		if e == nil {
			_, e = writer.Write([]byte(file.content))
		}
		if e != nil {
			http.Error(w, "部署包生成失败", 500)
			return
		}
	}
	if err := archive.Close(); err != nil {
		http.Error(w, "部署包生成失败", 500)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-https-deploy.zip"`, domain))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(buffer.Bytes())
}

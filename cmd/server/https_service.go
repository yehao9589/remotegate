package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type httpsSettings struct {
	Enabled         bool      `json:"enabled"`
	ListenAddress   string    `json:"listenAddress"`
	Mode            string    `json:"mode,omitempty"`
	FallbackAddress string    `json:"fallbackAddress,omitempty"`
	ProxyProtocol   bool      `json:"proxyProtocol,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
}

func normalizeHTTPSSettings(settings httpsSettings) (httpsSettings, error) {
	address, err := normalizeHTTPSAddress(settings.ListenAddress)
	if err != nil {
		return settings, err
	}
	settings.ListenAddress = address
	if settings.Mode == "" {
		settings.Mode = "direct"
	}
	if settings.Mode == "direct" {
		settings.FallbackAddress = ""
		settings.ProxyProtocol = false
		return settings, nil
	}
	if settings.Mode != "shared" {
		return settings, errors.New("请选择直接 HTTPS 或与宝塔共用入口")
	}
	fallback, err := normalizeHTTPSAddress(settings.FallbackAddress)
	if err != nil {
		return settings, errors.New("宝塔回源请填 127.0.0.1:9443 或 [::1]:9443")
	}
	host, port, _ := net.SplitHostPort(fallback)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return settings, errors.New("宝塔 HTTPS 回源仅允许本机回环地址")
	}
	_, listenPort, _ := net.SplitHostPort(address)
	if port == listenPort {
		return settings, errors.New("宝塔回源端口不能与公网监听端口相同，避免循环转发")
	}
	settings.FallbackAddress = fallback
	return settings, nil
}

func normalizeHTTPSAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	n, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || n < 1 || n > 65535 || (host != "" && net.ParseIP(host) == nil) {
		return "", errors.New("HTTPS 监听请填 :443、127.0.0.1:9443 或 [::]:443，端口为 1–65535")
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

func (a *app) httpsStatus() map[string]any {
	a.certMu.Lock()
	defer a.certMu.Unlock()
	return map[string]any{"enabled": a.httpsSettings.Enabled, "listenAddress": a.httpsSettings.ListenAddress, "mode": a.httpsSettings.Mode, "fallbackAddress": a.httpsSettings.FallbackAddress, "proxyProtocol": a.httpsSettings.ProxyProtocol, "address": a.httpsAddress, "error": a.httpsError, "running": a.httpsAddress != "", "updatedAt": a.httpsSettings.UpdatedAt}
}

func saveHTTPSSettings(path string, settings httpsSettings) error {
	if path == "" {
		return errors.New("HTTPS 配置存储未初始化")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".https-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Bind and save the replacement before releasing the old listener. A port conflict
// or failed disk write leaves the current HTTPS service and settings unchanged.
func (a *app) applyHTTPS(settings httpsSettings, persist bool) error {
	settings, err := normalizeHTTPSSettings(settings)
	if err != nil {
		return err
	}
	address := settings.ListenAddress
	if settings.Enabled && settings.Mode == "shared" && persist {
		backend, err := net.DialTimeout("tcp", settings.FallbackAddress, 3*time.Second)
		if err != nil {
			return fmt.Errorf("宝塔 HTTPS 后端 %s 未监听：%w；请先完成内部端口迁移，原入口保持不变", settings.FallbackAddress, err)
		}
		backend.Close()
	}
	a.certMu.Lock()
	defer a.certMu.Unlock()
	var listener net.Listener
	if settings.Enabled && (a.httpsServer == nil || a.httpsAddress != address) {
		listener, err = net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("HTTPS 无法监听 %s：%w；原入口保持不变", address, err)
		}
	}
	if persist {
		settings.UpdatedAt = time.Now().UTC()
		if err = saveHTTPSSettings(a.httpsSettingsPath, settings); err != nil {
			if listener != nil {
				listener.Close()
			}
			return fmt.Errorf("HTTPS 配置保存失败：%w", err)
		}
	}
	a.httpsSettings = settings
	if settings.Enabled && listener == nil {
		return nil
	}
	old := a.httpsServer
	a.httpsServer = nil
	a.httpsAddress = ""
	a.httpsError = ""
	if listener != nil {
		listener = newHTTPSIngress(listener, a.ingressRoute)
		server := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: a.selectHTTPSCertificate}}
		a.httpsServer = server
		a.httpsAddress = address
		go func() {
			err := server.ServeTLS(listener, "", "")
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.certMu.Lock()
				if a.httpsServer == server {
					a.httpsError = err.Error()
					a.httpsAddress = ""
					a.httpsServer = nil
				}
				a.certMu.Unlock()
				log.Printf("HTTPS listener stopped: %v", err)
			}
		}()
	}
	if old != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if old.Shutdown(ctx) != nil {
				_ = old.Close()
			}
		}()
	}
	return nil
}

func (a *app) selectHTTPSCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	for _, d := range a.store.Domains() {
		m, err := a.certificateFor(d.BaseDomain)
		if err == nil {
			if pair, err := m.getCertificate(hello); err == nil {
				return pair, nil
			}
		}
	}
	return nil, errors.New("no certificate installed for this domain")
}

func (a *app) initializeHTTPS() {
	value, configured := os.LookupEnv("HTTPS_LISTEN_ADDR")
	if !configured {
		value = ":443"
	}
	address := strings.TrimSpace(value)
	settings := httpsSettings{Enabled: address != "", ListenAddress: address}
	if address == "" {
		settings.ListenAddress = ":443"
	}
	if a.httpsSettingsPath != "" {
		raw, err := os.ReadFile(a.httpsSettingsPath)
		if err == nil {
			err = json.Unmarshal(raw, &settings)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			a.httpsError = "HTTPS 配置读取失败：" + err.Error()
			return
		}
	}
	a.httpsSettings = settings
	if err := a.applyHTTPS(settings, false); err != nil {
		a.httpsError = err.Error()
		log.Print("HTTPS listener could not start; console remains available")
	}
}

func (a *app) httpsSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		if address := r.URL.Query().Get("probe"); address != "" {
			address, err := normalizeHTTPSAddress(address)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			result := a.probeHTTPSAddress(address)
			if !result.Available {
				host, _, _ := net.SplitHostPort(address)
				for _, port := range []string{"18443", "24443", "34443"} {
					candidate := a.probeHTTPSAddress(net.JoinHostPort(host, port))
					if candidate.Available {
						result.Suggestion = candidate.Address
						break
					}
				}
			}
			writeJSON(w, result)
			return
		}
		writeJSON(w, a.httpsStatus())
		return
	}
	var settings httpsSettings
	if !authJSON(w, r, &settings) {
		return
	}
	if err := a.applyHTTPS(settings, true); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, a.httpsStatus())
}

type httpsAddressProbe struct {
	Address    string `json:"address"`
	Available  bool   `json:"available"`
	Current    bool   `json:"current"`
	Reason     string `json:"reason,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// A short bind checks this container's network namespace only. It never serves
// requests or replaces an existing listener; applyHTTPS rechecks when enabling.
func (a *app) probeHTTPSAddress(address string) httpsAddressProbe {
	a.certMu.Lock()
	defer a.certMu.Unlock()
	result := httpsAddressProbe{Address: address}
	if a.httpsServer != nil && a.httpsAddress == address {
		result.Available, result.Current = true, true
		return result
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	listener.Close()
	result.Available = true
	return result
}

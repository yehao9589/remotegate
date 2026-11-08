package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
)

func (a *app) proxyMappingWebSocket(w http.ResponseWriter, r *http.Request, mapping config.Mapping) {
	activityToken := r.URL.Query().Get(browserActivityQuery)
	if activityToken != "" {
		if !a.browserActivityValid(activityToken, mapping) {
			accessNoStore(w)
			http.Error(w, "页面已暂停，请点击继续查看", http.StatusGone)
			return
		}
		r = withoutActivityQuery(r)
	}
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "无效的 WebSocket 握手请求", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	resp, err := a.hub.RoundTripWebSocket(ctx, mapping.DeviceID, protocol.Message{
		Method: r.Method, Target: mapping.Target, Path: r.URL.RequestURI(), Headers: mappingProxyHeaders(r),
	}, mapping.ID)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, hub.ErrWebSocketBusy), errors.Is(err, hub.ErrHTTPBusy):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		default:
			http.Error(w, "无法建立 WebSocket 连接，请检查设备是否在线及插件是否支持网页流式转发", http.StatusBadGateway)
		}
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		// Preserve upstream rejections, including the controller's 401.
		timer := time.AfterFunc(30*time.Second, cancel)
		defer timer.Stop()
		writeMappedHTTPStream(w, r, mapping, resp)
		return
	}
	backend := resp.Body.(io.ReadWriteCloser) // RoundTripWebSocket validates 101 responses.
	defer backend.Close()
	if !a.mappingWebSocketAllowed(r, mapping) {
		http.Error(w, "映射配置或访问权限已变化，请重新连接", http.StatusForbidden)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "当前入口无法升级 WebSocket 连接", http.StatusBadGateway)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	setMappedResponseHeaders(w, r, mapping, protocol.FilterHeaders(resp.Header))
	w.Header().Set("Connection", "Upgrade")
	w.Header().Set("Upgrade", "websocket")
	handshake := &http.Response{StatusCode: http.StatusSwitchingProtocols, ProtoMajor: 1, ProtoMinor: 1, Header: w.Header()}
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if handshake.Write(rw) != nil || rw.Flush() != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, rw.Reader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(mappedSocketWriter{conn}, backend); done <- struct{}{} }()

	// A long-lived connection must stop after the mapping or its access changes.
	interval := 15 * time.Second
	if activityToken != "" {
		interval = 5 * time.Second
		if a.activityPollInterval > 0 {
			interval = a.activityPollInterval
		}
	}
	checkAccess := time.NewTicker(interval)
	defer checkAccess.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-checkAccess.C:
			if activityToken != "" && !a.browserActivityValid(activityToken, mapping) {
				return
			}
			if !a.mappingWebSocketAllowed(r, mapping) {
				return
			}
		}
	}
}

func (a *app) mappingWebSocketAllowed(r *http.Request, mapping config.Mapping) bool {
	current, exists := a.store.MappingForHost(r.Host)
	if !exists || current != mapping {
		return false
	}
	if mapping.AccessMode == "public" {
		return true
	}
	console, err := a.accessConsole(mapping)
	return err == nil && a.mappingSessionValid(r, mapping, console)
}

type mappedSocketWriter struct{ conn net.Conn }

func (w mappedSocketWriter) Write(p []byte) (int, error) {
	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.conn.Write(p)
}

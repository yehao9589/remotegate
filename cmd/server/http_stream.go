package main

import (
	"context"
	"errors"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
	"io"
	"log"
	"net/http"
	"time"
)

func (a *app) proxyStreamHTTP(w http.ResponseWriter, r *http.Request, m config.Mapping, body []byte) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	resp, err := a.hub.RoundTripHTTP(ctx, m.DeviceID, protocol.Message{Method: r.Method, Target: m.Target, Path: r.URL.RequestURI(), Headers: mappingProxyHeaders(r), Body: body}, m.ID)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if a.mappingRecoveryPage(w, r, m.DeviceID) {
			return
		}
		code := http.StatusBadGateway
		if errors.Is(err, hub.ErrHTTPBusy) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, "网页连接未完成，请稍后重试", code)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("X-Accel-Buffering", "no")
	writeMappedResponseHeaders(w, r, m, protocol.FilterHeaders(resp.Header), resp.StatusCode)
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	buffer := make([]byte, protocol.MaxStreamBytes)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err = w.Write(buffer[:n]); err != nil {
				return
			}
			if err = controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF && r.Context().Err() == nil {
				log.Printf("mapped HTTP stream interrupted: device=%s mapping=%s", m.DeviceID, m.ID)
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}
func writeMappedResponseHeaders(w http.ResponseWriter, r *http.Request, mapping config.Mapping, headers map[string][]string, status int) {
	rewriteLocation(headers, mapping.Target, r)
	for key, values := range headers {
		for _, value := range values {
			if http.CanonicalHeaderKey(key) == "Set-Cookie" && !mappingProxyCookie(value) {
				continue
			}
			w.Header().Add(key, value)
		}
	}
	if mapping.AccessMode != "public" {
		accessNoStore(w)
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Add("Vary", "Cookie")
	}
	if status < 100 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
}

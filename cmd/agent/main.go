package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

const version = "0.1.0"

type Config struct {
	ServerURL          string `json:"serverUrl"`
	DeviceID           string `json:"deviceId"`
	Token              string `json:"token"`
	Interface          string `json:"interface,omitempty"`
	Mark               int    `json:"mark,omitempty"`
	InsecureTLS        bool   `json:"insecureTls,omitempty"`
	AllowPublicTargets bool   `json:"allowPublicTargets,omitempty"`
}

func main() {
	path := "/etc/remotegate/agent.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var cfg Config
	if err = json.Unmarshal(data, &cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.ServerURL == "" || cfg.DeviceID == "" || cfg.Token == "" {
		log.Fatal("serverUrl, deviceId and token are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	backoff := time.Second
	for ctx.Err() == nil {
		if err = run(ctx, cfg); err != nil && ctx.Err() == nil {
			log.Printf("connection ended: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func run(ctx context.Context, cfg Config) error {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	u.Path = "/api/agent/connect"
	q := u.Query()
	q.Set("device_id", cfg.DeviceID)
	q.Set("token", cfg.Token)
	q.Set("version", version)
	u.RawQuery = q.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureTLS}}
	dialer.NetDialContext = boundDialer(cfg.Interface, cfg.Mark).DialContext
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("connect: %s", resp.Status)
		}
		return err
	}
	defer conn.Close()
	log.Printf("connected to %s", u.Host)
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}},
		Timeout:   55 * time.Second,
		// Redirects must be returned to the remote browser. Following them here
		// loses authentication cookies set by applications such as LuCI.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for {
		var msg protocol.Message
		if err = conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Type != "request" {
			continue
		}
		result := handle(client, cfg, msg)
		if err = conn.WriteJSON(result); err != nil {
			return err
		}
	}
}

func handle(client *http.Client, cfg Config, msg protocol.Message) protocol.Message {
	out := protocol.Message{Type: "response", ID: msg.ID}
	base, err := url.Parse(msg.Target)
	if err != nil {
		out.Error = "invalid mapping target"
		return out
	}
	if !cfg.AllowPublicTargets && !privateTarget(base.Hostname()) {
		out.Error = "target is outside private address space"
		return out
	}
	rel, err := url.Parse(msg.Path)
	if err != nil {
		out.Error = "invalid request path"
		return out
	}
	target := base.ResolveReference(rel)
	req, err := http.NewRequest(msg.Method, target.String(), strings.NewReader(string(msg.Body)))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	for key, values := range msg.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Host = base.Host
	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyBytes+1))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(body) > protocol.MaxBodyBytes {
		out.Error = "response too large"
		return out
	}
	out.Status = resp.StatusCode
	out.Headers = protocol.FilterHeaders(resp.Header)
	out.Body = body
	return out
}

func privateTarget(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			return false
		}
	}
	return true
}

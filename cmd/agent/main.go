package main

import (
	"bytes"
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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/tcpbridge"
)

type Config struct {
	ServerURL          string `json:"serverUrl"`
	DeviceID           string `json:"deviceId"`
	Token              string `json:"token"`
	Interface          string `json:"interface,omitempty"`
	Mark               int    `json:"mark,omitempty"`
	DirectWAN          bool   `json:"directWAN,omitempty"`
	InsecureTLS        bool   `json:"insecureTls,omitempty"`
	AllowPublicTargets bool   `json:"allowPublicTargets,omitempty"`
	RemoteAccess       bool   `json:"remoteAccess"`
}

func main() {
	if handleUpdateCLI(os.Args[1:]) {
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Printf("RemoteGate Agent %s (%s)\n", buildinfo.Tag(), buildinfo.Commit)
		return
	}
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
	status := newConnectionReporter(routerConnectionPath, cfg)
	defer status.stopped()
	serverDial := newServerDialer(cfg, status.resolution)
	serverDial.routed = status.route
	if cfg.DirectWAN {
		go maintainDirectRoute(ctx, cfg)
	}
	var backoff time.Duration
	for ctx.Err() == nil {
		started := time.Now()
		status.attempting()
		err = runSession(ctx, cfg, serverDial.DialContext, status)
		if err != nil && ctx.Err() == nil {
			log.Printf("connection ended: %s", safeConnectionError(err, cfg.Token))
		}
		backoff = nextReconnectDelay(backoff, time.Since(started))
		if ctx.Err() == nil {
			status.disconnected(err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func nextReconnectDelay(previous, sessionDuration time.Duration) time.Duration {
	if sessionDuration >= 30*time.Second || previous <= 0 {
		return time.Second
	}
	if previous >= 15*time.Second {
		return 30 * time.Second
	}
	return previous * 2
}

func run(ctx context.Context, cfg Config) error {
	return runSession(ctx, cfg, newServerDialer(cfg, nil).DialContext, nil)
}

func runSession(ctx context.Context, cfg Config, serverDial dialContextFunc, status *connectionReporter) error {
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
	q.Set("version", buildinfo.Version())
	q.Set("info", "1")
	if remoteTransportAllowed(cfg) {
		q.Set("remote", "1")
	}
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	q.Set("interface", cfg.Interface)
	if hostname, err := os.Hostname(); err == nil && len(hostname) <= 128 {
		q.Set("hostname", hostname)
	}
	if routerUpdateSupported() {
		q.Set("updater", "1")
		q.Set("update_tools", "1")
	}
	if installed, err := os.ReadFile("/usr/share/remotegate/version"); err == nil {
		value := strings.TrimSpace(string(installed))
		if len(value) > 0 && len(value) <= 64 {
			q.Set("package_version", value)
		}
	}
	u.RawQuery = q.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureTLS}}
	var egressInterface string
	dialer.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		socket, err := serverDial(ctx, network, address)
		if err == nil {
			egressInterface = socketEgressInterface(socket)
		}
		return socket, err
	}
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("connect: %s", resp.Status)
		}
		return err
	}
	defer conn.Close()
	conn.SetReadLimit(protocol.MaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	ping := conn.PingHandler()
	conn.SetPingHandler(func(data string) error {
		if err := ping(data); err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		status.heartbeat()
		return nil
	})
	status.connected()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	if egressInterface != "" {
		if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return err
		}
		if err := conn.WriteJSON(protocol.Message{Type: "device-network", EgressInterface: egressInterface}); err != nil {
			return err
		}
	}
	log.Printf("connected to %s", u.Host)
	client := targetHTTPClient(cfg)
	sessionContext, stopSession := context.WithCancel(ctx)
	var writeMu sync.Mutex
	send := func(result protocol.Message) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		err := conn.WriteJSON(result)
		if err != nil {
			_ = conn.Close()
		}
		return err
	}
	bridge := tcpbridge.New(sessionContext, remoteTransportAllowed(cfg), send)
	var workers sync.WaitGroup
	queue := make(chan protocol.Message, 32)
	var queuedBytes atomic.Int64
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-sessionContext.Done():
					return
				case msg := <-queue:
					var result protocol.Message
					if msg.Type == "request" {
						result = handleWithContext(sessionContext, client, cfg, msg)
					} else {
						result = updateControl(sessionContext, cfg, msg)
					}
					_ = send(result)
					queuedBytes.Add(-queuedMessageBytes(msg))
				}
			}
		}()
	}
	defer func() { stopSession(); _ = conn.Close(); bridge.Close(); workers.Wait(); client.CloseIdleConnections() }()
	for {
		var msg protocol.Message
		if err = conn.ReadJSON(&msg); err != nil {
			return err
		}
		if bridge.Handle(msg) {
			continue
		}
		if msg.Type != "request" && msg.Type != "update-start" && msg.Type != "update-status" && msg.Type != "update-preflight" {
			continue
		}
		size := queuedMessageBytes(msg)
		if queuedBytes.Add(size) > protocol.MaxBodyBytes+(1<<20) {
			queuedBytes.Add(-size)
			_ = send(protocol.Message{Type: "response", ID: msg.ID, Error: "device is busy, please retry"})
			continue
		}
		select {
		case queue <- msg:
		default:
			queuedBytes.Add(-size)
			_ = send(protocol.Message{Type: "response", ID: msg.ID, Error: "device is busy, please retry"})
		}
	}
}

func queuedMessageBytes(msg protocol.Message) int64 {
	size := len(msg.Body) + len(msg.Target) + len(msg.Path) + len(msg.Method) + 2048
	for key, values := range msg.Headers {
		size += len(key)
		for _, value := range values {
			size += len(value)
		}
	}
	return int64(size)
}

func handle(client *http.Client, cfg Config, msg protocol.Message) protocol.Message {
	return handleWithContext(context.Background(), client, cfg, msg)
}

func handleWithContext(ctx context.Context, client *http.Client, cfg Config, msg protocol.Message) protocol.Message {
	out := protocol.Message{Type: "response", ID: msg.ID}
	base, err := url.Parse(msg.Target)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil {
		out.Error = "invalid mapping target"
		return out
	}
	if !cfg.AllowPublicTargets && !privateTarget(base.Hostname()) {
		out.Error = "target is outside private address space"
		return out
	}
	rel, err := url.Parse(msg.Path)
	if err != nil || rel.IsAbs() || rel.Host != "" || !strings.HasPrefix(msg.Path, "/") || rel.Fragment != "" {
		out.Error = "invalid request path"
		return out
	}
	target := base.ResolveReference(rel)
	req, err := http.NewRequestWithContext(ctx, msg.Method, target.String(), bytes.NewReader(msg.Body))
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

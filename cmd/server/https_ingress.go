package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errClientHelloRead = errors.New("ClientHello inspection complete")

// The standard TLS parser extracts SNI. Its deliberate abort and alert never
// reach the network; the original bytes are replayed to the chosen TLS endpoint.
type helloCapture struct {
	net.Conn
	data bytes.Buffer
}

func (c *helloCapture) Read(p []byte) (int, error) {
	remaining := (64 << 10) - c.data.Len()
	if remaining <= 0 {
		return 0, errors.New("ClientHello exceeds inspection limit")
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := c.Conn.Read(p)
	c.data.Write(p[:n])
	return n, err
}
func (c *helloCapture) Write(p []byte) (int, error) { return len(p), nil }

type helloReplay struct {
	net.Conn
	data *bytes.Reader
}

func (c *helloReplay) Read(p []byte) (int, error) {
	if c.data.Len() > 0 {
		return c.data.Read(p)
	}
	return c.Conn.Read(p)
}

func inspectTLSHello(conn net.Conn) (string, net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	captured := &helloCapture{Conn: conn}
	host := ""
	inspector := tls.Server(captured, &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		host = strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
		return nil, errClientHelloRead
	}})
	err := inspector.Handshake()
	if !errors.Is(err, errClientHelloRead) {
		return "", nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return host, &helloReplay{Conn: conn, data: bytes.NewReader(captured.data.Bytes())}, nil
}

type ingressChoice struct {
	native        bool
	fallback      string
	proxyProtocol bool
}

func (a *app) ingressRoute(host string) ingressChoice {
	a.certMu.Lock()
	settings := a.httpsSettings
	a.certMu.Unlock()
	if settings.Mode != "shared" {
		return ingressChoice{native: true}
	}
	choice := ingressChoice{fallback: settings.FallbackAddress, proxyProtocol: settings.ProxyProtocol}
	// Every configured mapping remains owned here, including offline/disabled
	// ones. A missing certificate must fail here, not present a Baota default cert.
	for _, mapping := range a.store.Snapshot().Mappings {
		if mapping.Host == host {
			choice.native = true
			return choice
		}
	}
	for _, domain := range a.store.Domains() {
		if host == domain.BaseDomain && domain.RootHTTPSProvider == "external" {
			continue
		}
		if host == a.consoleHost && domain.RootHTTPSProvider != "remotegate" {
			continue
		}
		if host != domain.BaseDomain && !strings.HasSuffix(host, "."+domain.BaseDomain) {
			continue
		}
		manager, err := a.certificateFor(domain.BaseDomain)
		if err == nil {
			if _, err = manager.getCertificate(&tls.ClientHelloInfo{ServerName: host}); err == nil {
				choice.native = true
				return choice
			}
		}
	}
	return choice
}

// Raw acceptance and inspection run separately so a slow ClientHello cannot
// block other sites. In-flight connections are bounded and closed on shutdown.
type httpsIngress struct {
	net.Listener
	route  func(string) ingressChoice
	ready  chan net.Conn
	done   chan struct{}
	slots  chan struct{}
	mu     sync.Mutex
	active map[net.Conn]bool
	closed bool
	once   sync.Once
}

func newHTTPSIngress(listener net.Listener, route func(string) ingressChoice) *httpsIngress {
	i := &httpsIngress{Listener: listener, route: route, ready: make(chan net.Conn), done: make(chan struct{}), slots: make(chan struct{}, 256), active: make(map[net.Conn]bool)}
	go i.receive()
	return i
}
func (i *httpsIngress) track(conn net.Conn, tunnel bool) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		conn.Close()
		return false
	}
	i.active[conn] = tunnel
	return true
}
func (i *httpsIngress) forget(conn net.Conn) { i.mu.Lock(); delete(i.active, conn); i.mu.Unlock() }
func (i *httpsIngress) receive() {
	for {
		conn, err := i.Listener.Accept()
		if err != nil {
			i.Close()
			return
		}
		select {
		case i.slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		if !i.track(conn, false) {
			<-i.slots
			return
		}
		go i.dispatch(conn)
	}
}
func (i *httpsIngress) dispatch(raw net.Conn) {
	defer func() { <-i.slots; i.forget(raw) }()
	conn := raw
	choice := i.route("")
	if !choice.native {
		host, replay, err := inspectTLSHello(raw)
		if err != nil {
			raw.Close()
			return
		}
		conn = replay
		choice = i.route(host)
	}
	if choice.native {
		select {
		case i.ready <- conn:
		case <-i.done:
			conn.Close()
		}
		return
	}
	backend, err := net.DialTimeout("tcp", choice.fallback, 4*time.Second)
	if err != nil {
		conn.Close()
		return
	}
	if !i.track(backend, true) {
		conn.Close()
		return
	}
	i.mu.Lock()
	i.active[raw] = true
	i.mu.Unlock()
	defer i.forget(backend)
	defer backend.Close()
	defer conn.Close()
	if choice.proxyProtocol {
		if _, err = io.WriteString(backend, proxyProtocolHeader(raw)); err != nil {
			return
		}
	}
	completed := make(chan struct{})
	go func() {
		_, _ = io.Copy(backend, conn)
		if tcp, ok := backend.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		close(completed)
	}()
	_, _ = io.Copy(conn, backend)
	conn.Close()
	backend.Close()
	<-completed
}
func (i *httpsIngress) Accept() (net.Conn, error) {
	select {
	case conn := <-i.ready:
		return conn, nil
	case <-i.done:
		return nil, net.ErrClosed
	}
}
func (i *httpsIngress) Close() error {
	var err error
	i.once.Do(func() {
		i.mu.Lock()
		i.closed = true
		close(i.done)
		err = i.Listener.Close()
		for conn, tunnel := range i.active {
			if !tunnel {
				conn.Close()
			}
		}
		i.mu.Unlock()
		// Let a settings response sent through Baota finish before terminating
		// passthrough connections. This does not keep the public listener open.
		time.AfterFunc(10*time.Second, func() {
			i.mu.Lock()
			defer i.mu.Unlock()
			for conn := range i.active {
				conn.Close()
			}
		})
	})
	return err
}
func proxyProtocolHeader(conn net.Conn) string {
	source, sok := conn.RemoteAddr().(*net.TCPAddr)
	destination, dok := conn.LocalAddr().(*net.TCPAddr)
	if !sok || !dok {
		return "PROXY UNKNOWN\r\n"
	}
	family := "TCP6"
	src, dst := source.IP, destination.IP
	if src.To4() != nil && dst.To4() != nil {
		family = "TCP4"
		src = src.To4()
		dst = dst.To4()
	} else if src.To4() != nil || dst.To4() != nil {
		return "PROXY UNKNOWN\r\n"
	}
	return fmt.Sprintf("PROXY %s %s %s %s %s\r\n", family, src.String(), dst.String(), strconv.Itoa(source.Port), strconv.Itoa(destination.Port))
}

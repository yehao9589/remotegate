package tcpbridge

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate/internal/protocol"
)

type Bridge struct {
	ctx     context.Context
	enabled bool
	send    func(protocol.Message) error
	mu      sync.Mutex
	streams map[string]*agentTCP
	wg      sync.WaitGroup
}
type agentTCP struct {
	bridge   *Bridge
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conn     net.Conn
	incoming chan []byte
	credit   int
	changed  chan struct{}
	once     sync.Once
}

func New(ctx context.Context, enabled bool, send func(protocol.Message) error) *Bridge {
	return &Bridge{ctx: ctx, enabled: enabled, send: send, streams: make(map[string]*agentTCP)}
}

func (b *Bridge) Handle(msg protocol.Message) bool {
	if !strings.HasPrefix(msg.Type, "tcp-") {
		return false
	}
	if msg.Type == "tcp-open" {
		b.mu.Lock()
		_, duplicate := b.streams[msg.ID]
		valid := b.enabled && len(msg.ID) == 36 && strings.HasPrefix(msg.ID, "tcp:") && !duplicate && len(b.streams) < protocol.MaxDeviceStreams
		if !valid {
			b.mu.Unlock()
			_ = b.send(protocol.Message{Type: "response", ID: msg.ID, Error: "远程连接未启用或连接数量已达上限"})
			return true
		}
		ctx, cancel := context.WithCancel(b.ctx)
		s := &agentTCP{bridge: b, id: msg.ID, ctx: ctx, cancel: cancel, incoming: make(chan []byte, 8), credit: protocol.StreamWindowBytes, changed: make(chan struct{}, 1)}
		b.streams[msg.ID] = s
		b.wg.Add(1)
		b.mu.Unlock()
		go func() { defer b.wg.Done(); s.run(msg.Target) }()
		return true
	}
	b.mu.Lock()
	s := b.streams[msg.ID]
	b.mu.Unlock()
	if s == nil {
		return true
	}
	switch msg.Type {
	case "tcp-close":
		s.close(false)
	case "tcp-credit":
		s.mu.Lock()
		valid := msg.Status > 0 && msg.Status <= protocol.StreamWindowBytes && s.credit+msg.Status <= protocol.StreamWindowBytes
		if valid {
			s.credit += msg.Status
		}
		s.mu.Unlock()
		if !valid {
			s.close(true)
		}
		select {
		case s.changed <- struct{}{}:
		default:
		}
	case "tcp-data":
		if len(msg.Body) == 0 {
			return true
		}
		if len(msg.Body) > protocol.MaxStreamBytes {
			s.close(true)
			return true
		}
		select {
		case <-s.ctx.Done():
		case s.incoming <- msg.Body:
		default:
			s.close(true)
		}
	}
	return true
}

func dialPrivateTCP(ctx context.Context, target string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(target)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || len(host) == 0 || len(host) > 253 {
		return nil, errors.New("SSH 地址或端口无效")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("无法解析 SSH 内网地址")
	}
	for _, address := range addresses {
		ip := address.IP
		if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			return nil, errors.New("仅允许连接设备本机或内网 SSH 服务")
		}
	}
	// Dial resolved addresses, rather than resolving the hostname a second time.
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, address := range addresses {
		ip := address.IP.String()
		if address.Zone != "" {
			ip += "%" + address.Zone
		}
		conn, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
		if dialErr == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("无法连接 SSH 服务，请检查内网地址、端口和服务状态")
}

func (s *agentTCP) run(target string) {
	defer s.close(true)
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	conn, err := dialPrivateTCP(ctx, target)
	cancel()
	if err != nil {
		_ = s.bridge.send(protocol.Message{Type: "response", ID: s.id, Error: err.Error()})
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		_ = conn.Close()
		return
	}
	if s.bridge.send(protocol.Message{Type: "response", ID: s.id}) != nil {
		return
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer s.close(true)
		buffer := make([]byte, protocol.MaxStreamBytes)
		for {
			n, err := conn.Read(buffer)
			if n > 0 {
				if !s.takeCredit(n) || s.bridge.send(protocol.Message{Type: "tcp-data", ID: s.id, Body: buffer[:n]}) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { s.close(true); <-readDone }()
	for {
		select {
		case <-s.ctx.Done():
			return
		case body := <-s.incoming:
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			remaining := body
			for len(remaining) != 0 {
				n, writeErr := conn.Write(remaining)
				if writeErr != nil || n == 0 {
					return
				}
				remaining = remaining[n:]
			}
			if s.bridge.send(protocol.Message{Type: "tcp-credit", ID: s.id, Status: len(body)}) != nil {
				return
			}
		}
	}
}

func (s *agentTCP) takeCredit(n int) bool {
	for {
		s.mu.Lock()
		if s.credit >= n {
			s.credit -= n
			s.mu.Unlock()
			return true
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return false
		case <-s.changed:
		}
	}
}
func (s *agentTCP) close(notify bool) {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.mu.Unlock()
		s.bridge.mu.Lock()
		delete(s.bridge.streams, s.id)
		s.bridge.mu.Unlock()
		if notify && s.bridge.ctx.Err() == nil {
			_ = s.bridge.send(protocol.Message{Type: "tcp-close", ID: s.id})
		}
	})
}
func (b *Bridge) Close() {
	b.mu.Lock()
	streams := make([]*agentTCP, 0, len(b.streams))
	for _, stream := range b.streams {
		streams = append(streams, stream)
	}
	b.mu.Unlock()
	for _, stream := range streams {
		stream.close(false)
	}
	b.wg.Wait()
}

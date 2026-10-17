package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/local/remotegate/internal/protocol"
)

type tunnelAddr string

func (a tunnelAddr) Network() string { return "remotegate" }
func (a tunnelAddr) String() string  { return string(a) }

// TCP connections share the authenticated device tunnel, with bounded queues.
type tcpConn struct {
	client          *Client
	id, target      string
	incoming        chan []byte
	done, changed   chan struct{}
	once            sync.Once
	readMu, mu      sync.Mutex
	buffer          []byte
	cause           error
	readAt, writeAt time.Time
	credit          int
	creditChanged   chan struct{}
	creditReady     chan struct{}
	consumed        atomic.Int64
}

func (h *Hub) OpenTCP(ctx context.Context, deviceID, target string) (net.Conn, error) {
	h.mu.RLock()
	c := h.clients[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return nil, ErrOffline
	}
	if !c.CanRemote {
		return nil, errors.New("请升级插件，并在路由器插件中启用 SSH / 文件管理")
	}
	if c.streamCount.Add(1) > protocol.MaxDeviceStreams {
		c.streamCount.Add(-1)
		return nil, errors.New("这台设备的远程连接数量已达上限")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		c.streamCount.Add(-1)
		return nil, err
	}
	id := "tcp:" + hex.EncodeToString(nonce[:])
	stream := &tcpConn{client: c, id: id, target: target, incoming: make(chan []byte, 8), done: make(chan struct{}), changed: make(chan struct{}, 1), credit: protocol.StreamWindowBytes, creditChanged: make(chan struct{}, 1), creditReady: make(chan struct{}, 1)}
	c.streams.Store(id, stream)
	reply, err := h.requestTo(ctx, c, protocol.Message{Type: "tcp-open", ID: id, Target: target})
	if err == nil && reply.Error != "" {
		err = errors.New(reply.Error)
	}
	if err != nil {
		stream.Close()
		return nil, err
	}
	go stream.returnCredits()
	return stream, nil
}

func (c *tcpConn) feed(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	if len(body) > protocol.MaxStreamBytes {
		return false
	}
	select {
	case <-c.done:
		return true
	case c.incoming <- body:
		return true
	default:
		return false
	}
}

func (c *tcpConn) finish(err error, notify bool) {
	c.once.Do(func() {
		c.mu.Lock()
		c.cause = err
		c.mu.Unlock()
		c.client.streams.Delete(c.id)
		c.client.streamCount.Add(-1)
		close(c.done)
		if notify {
			go func() { _ = c.send(protocol.Message{Type: "tcp-close", ID: c.id}, time.Now().Add(5*time.Second)) }()
		}
	})
}

func (c *tcpConn) send(msg protocol.Message, deadline time.Time) error {
	c.client.writeMu.Lock()
	defer c.client.writeMu.Unlock()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	if err := c.client.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := c.client.conn.WriteJSON(msg); err != nil {
		_ = c.client.conn.Close()
		return err
	}
	return nil
}

func (c *tcpConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.buffer) == 0 {
		c.mu.Lock()
		deadline := c.readAt
		c.mu.Unlock()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		// Preserve final queued SSH packets before reporting an orderly EOF.
		select {
		case c.buffer = <-c.incoming:
			continue
		default:
		}
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case c.buffer = <-c.incoming:
		case <-c.changed:
		case <-timeout:
			if timer != nil {
				timer.Stop()
			}
			return 0, os.ErrDeadlineExceeded
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			select {
			case c.buffer = <-c.incoming:
				continue
			default:
			}
			c.mu.Lock()
			err := c.cause
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		if timer != nil {
			timer.Stop()
		}
	}
	n := copy(p, c.buffer)
	c.buffer = c.buffer[n:]
	c.consumed.Add(int64(n))
	select {
	case c.creditReady <- struct{}{}:
	default:
	}
	return n, nil
}

func (c *tcpConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) != 0 {
		select {
		case <-c.done:
			return written, io.ErrClosedPipe
		default:
		}
		c.mu.Lock()
		deadline := c.writeAt
		c.mu.Unlock()
		if deadline.IsZero() {
			deadline = time.Now().Add(30 * time.Second)
		}
		n := len(p)
		if n > protocol.MaxStreamBytes {
			n = protocol.MaxStreamBytes
		}
		if err := c.takeCredit(n, deadline); err != nil {
			return written, err
		}
		if err := c.send(protocol.Message{Type: "tcp-data", ID: c.id, Body: p[:n]}, deadline); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *tcpConn) Close() error {
	c.finish(net.ErrClosed, true)
	return nil
}
func (c *tcpConn) LocalAddr() net.Addr  { return tunnelAddr(c.client.DeviceID) }
func (c *tcpConn) RemoteAddr() net.Addr { return tunnelAddr(c.target) }
func (c *tcpConn) SetDeadline(at time.Time) error {
	c.mu.Lock()
	c.readAt, c.writeAt = at, at
	c.mu.Unlock()
	c.wake()
	select {
	case c.creditChanged <- struct{}{}:
	default:
	}
	return nil
}
func (c *tcpConn) SetReadDeadline(at time.Time) error {
	c.mu.Lock()
	c.readAt = at
	c.mu.Unlock()
	c.wake()
	return nil
}
func (c *tcpConn) SetWriteDeadline(at time.Time) error {
	c.mu.Lock()
	c.writeAt = at
	c.mu.Unlock()
	select {
	case c.creditChanged <- struct{}{}:
	default:
	}
	return nil
}
func (c *tcpConn) wake() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *tcpConn) addCredit(n int) bool {
	c.mu.Lock()
	valid := n > 0 && n <= protocol.StreamWindowBytes && c.credit+n <= protocol.StreamWindowBytes
	if valid {
		c.credit += n
	}
	c.mu.Unlock()
	select {
	case c.creditChanged <- struct{}{}:
	default:
	}
	return valid
}

func (c *tcpConn) takeCredit(n int, deadline time.Time) error {
	for {
		c.mu.Lock()
		if !c.writeAt.IsZero() {
			deadline = c.writeAt
		}
		if !time.Now().Before(deadline) {
			c.mu.Unlock()
			return os.ErrDeadlineExceeded
		}
		if c.credit >= n {
			c.credit -= n
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-c.creditChanged:
			timer.Stop()
		case <-c.done:
			timer.Stop()
			return net.ErrClosed
		case <-timer.C:
			return os.ErrDeadlineExceeded
		}
	}
}

func (c *tcpConn) returnCredits() {
	for {
		select {
		case <-c.done:
			return
		case <-c.creditReady:
			n := c.consumed.Swap(0)
			if n != 0 {
				if err := c.send(protocol.Message{Type: "tcp-credit", ID: c.id, Status: int(n)}, time.Now().Add(10*time.Second)); err != nil {
					c.finish(err, false)
					return
				}
			}
		}
	}
}

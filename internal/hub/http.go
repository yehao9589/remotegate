package hub

import (
	"bytes"
	"context"
	"errors"
	"github.com/local/remotegate/internal/protocol"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"time"
)

var ErrHTTPBusy = errors.New("设备网页连接较多，请稍后重试")

func (h *Hub) StreamingHTTP(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c := h.clients[id]
	return c != nil && c.CanStreamHTTP
}

// Each response has a credit-limited stream. A slow browser cannot make the
// device allocate an entire response or block the shared WebSocket reader.
func (h *Hub) RoundTripHTTP(ctx context.Context, id string, msg protocol.Message, mapping string) (*http.Response, error) {
	base, err := url.Parse(msg.Target)
	if err != nil || base.Hostname() == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("invalid mapping target")
	}
	rel, err := url.Parse(msg.Path)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.Fragment != "" || len(msg.Path) == 0 || msg.Path[0] != '/' {
		return nil, errors.New("invalid request path")
	}
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	var raw net.Conn
	waiting := false
	defer func() {
		if waiting {
			h.mu.Lock()
			h.httpWaiting--
			h.mu.Unlock()
		}
	}()
	for {
		raw, err = h.openStream(openCtx, id, msg.Target, true)
		if !errors.Is(err, ErrHTTPBusy) {
			break
		}
		if !waiting {
			h.mu.Lock()
			if h.httpWaiting >= 64 {
				h.mu.Unlock()
				cancel()
				return nil, ErrHTTPBusy
			}
			h.httpWaiting++
			h.mu.Unlock()
			waiting = true
		}
		select {
		case <-openCtx.Done():
			cancel()
			return nil, openCtx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	cancel()
	if err != nil {
		return nil, err
	}
	conn := raw.(*tcpConn)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	var used atomic.Bool
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ResponseHeaderTimeout: 25 * time.Second, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		if err := dialCtx.Err(); err != nil {
			return nil, err
		}
		if !used.CompareAndSwap(false, true) {
			return nil, errors.New("HTTP request must not be replayed")
		}
		return conn, nil
	}}
	cleanup := func() { stop(); _ = conn.Close(); transport.CloseIdleConnections() }
	target := base.ResolveReference(rel)
	// TLS to the private service is established on the agent, not on this virtual
	// connection. Preserve the target authority and its original Host header.
	target.Scheme = "http"
	body := &countedHTTPBody{Reader: bytes.NewReader(msg.Body), count: func(n int) {
		if conn.client.record != nil {
			conn.client.record(mapping, uint64(n), 0)
		}
	}}
	req, err := http.NewRequestWithContext(ctx, msg.Method, target.String(), body)
	if err != nil {
		cleanup()
		return nil, err
	}
	req.ContentLength = int64(len(msg.Body))
	if len(msg.Body) == 0 {
		req.Body = http.NoBody
	}
	req.Header = http.Header(protocol.FilterHeaders(http.Header(msg.Headers)))
	req.Host = base.Host
	resp, err := transport.RoundTrip(req)
	if err != nil {
		cleanup()
		return nil, err
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		resp.Body.Close()
		cleanup()
		return nil, errors.New("WebSocket mapping is not supported")
	}
	resp.Body = &streamHTTPBody{ReadCloser: resp.Body, conn: conn, cleanup: cleanup, count: func(n int) {
		if conn.client.record != nil {
			conn.client.record(mapping, 0, uint64(n))
		}
	}}
	return resp, nil
}

type countedHTTPBody struct {
	io.Reader
	count func(int)
}

func (b *countedHTTPBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if n > 0 {
		b.count(n)
	}
	return n, err
}

type streamHTTPBody struct {
	io.ReadCloser
	conn    net.Conn
	cleanup func()
	count   func(int)
}

func (b *streamHTTPBody) Read(p []byte) (int, error) {
	_ = b.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.count(n)
	}
	return n, err
}
func (b *streamHTTPBody) Close() error { b.cleanup(); return b.ReadCloser.Close() }

func (c *tcpConn) readWeb(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if n := c.webBuffer.Pop(p); n > 0 {
			c.consumed.Add(int64(n))
			select {
			case c.creditReady <- struct{}{}:
			default:
			}
			return n, nil
		}
		select {
		case <-c.done:
			c.mu.Lock()
			err := c.cause
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		default:
		}
		c.mu.Lock()
		deadline := c.readAt
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			if !time.Now().Before(deadline) {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-c.webBuffer.Ready():
		case <-c.changed:
		case <-c.done:
		case <-timeout:
			if timer != nil {
				timer.Stop()
			}
			return 0, os.ErrDeadlineExceeded
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

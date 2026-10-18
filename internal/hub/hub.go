package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

var ErrOffline = errors.New("device is offline")

type Client struct {
	DeviceID                string
	Name                    string
	Version                 string
	Since                   time.Time
	CanUpdate               bool
	CanRemote               bool
	CanManageUpdate         bool
	conn                    *websocket.Conn
	writeMu                 sync.Mutex
	pending                 sync.Map
	pings                   sync.Map
	streams                 sync.Map
	streamCount             atomic.Int32
	metricsMu               sync.Mutex
	lastActiveAt, latencyAt time.Time
	latency                 time.Duration
	heartbeat               string
	heartbeatAt             time.Time
	record                  func(string, uint64, uint64)
}

type pendingResponse struct {
	reply   chan protocol.Message
	mapping string
	account bool
}

type pingResult struct {
	latency time.Duration
	err     error
}
type pingWaiter struct {
	started time.Time
	result  chan pingResult
}

type Hub struct {
	mu           sync.RWMutex
	clients      map[string]*Client
	pingInterval time.Duration
	pongWait     time.Duration
	writeWait    time.Duration
	record       func(string, string, uint64, uint64)
}

func New(recorder ...func(string, string, uint64, uint64)) *Hub {
	h := &Hub{clients: make(map[string]*Client), pingInterval: 20 * time.Second, pongWait: 90 * time.Second, writeWait: 30 * time.Second}
	if len(recorder) > 0 {
		h.record = recorder[0]
	}
	return h
}

func (h *Hub) Disconnect(id string) {
	h.mu.RLock()
	c := h.clients[id]
	h.mu.RUnlock()
	if c != nil {
		_ = c.conn.Close()
		h.Detach(c)
	}
}

func (h *Hub) Attach(deviceID, name, version string, conn *websocket.Conn, updater ...bool) *Client {
	c := &Client{DeviceID: deviceID, Name: name, Version: version, Since: time.Now().UTC(), conn: conn}
	if h.record != nil {
		c.record = func(mapping string, up, down uint64) { h.record(deviceID, mapping, up, down) }
	}
	c.CanUpdate = len(updater) > 0 && updater[0]
	c.CanRemote = len(updater) > 1 && updater[1]
	c.CanManageUpdate = len(updater) > 2 && updater[2]
	c.lastActiveAt = c.Since
	h.mu.Lock()
	old := h.clients[deviceID]
	h.clients[deviceID] = c
	h.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	return c
}

func (h *Hub) Detach(c *Client) {
	h.mu.Lock()
	if h.clients[c.DeviceID] == c {
		delete(h.clients, c.DeviceID)
	}
	h.mu.Unlock()
	c.pending.Range(func(key, value any) bool {
		if actual, ok := c.pending.LoadAndDelete(key); ok {
			actual.(pendingResponse).reply <- protocol.Message{Type: "response", ID: key.(string), Error: "device disconnected"}
		}
		return true
	})
	c.pings.Range(func(key, value any) bool {
		if actual, ok := c.pings.LoadAndDelete(key); ok {
			actual.(pingWaiter).result <- pingResult{err: ErrOffline}
		}
		return true
	})
	c.streams.Range(func(_, value any) bool {
		value.(*tcpConn).finish(ErrOffline, false)
		return true
	})
}

func (h *Hub) ReadLoop(c *Client) error {
	// Server-originated traffic keeps reverse proxies from closing idle tunnels.
	// WebSocket control frames work with the default pong handler in old agents.
	if err := c.conn.SetReadDeadline(time.Now().Add(h.pongWait)); err != nil {
		return err
	}
	c.conn.SetPongHandler(func(data string) error {
		now := time.Now()
		c.metricsMu.Lock()
		c.lastActiveAt = now.UTC()
		if data == c.heartbeat && data != "" {
			c.latency, c.latencyAt = now.Sub(c.heartbeatAt), now.UTC()
		}
		c.metricsMu.Unlock()
		if value, ok := c.pings.LoadAndDelete(data); ok {
			waiter := value.(pingWaiter)
			latency := now.Sub(waiter.started)
			c.metricsMu.Lock()
			c.latency, c.latencyAt = latency, now.UTC()
			c.metricsMu.Unlock()
			waiter.result <- pingResult{latency: latency}
		}
		return c.conn.SetReadDeadline(time.Now().Add(h.pongWait))
	})
	stop := make(chan struct{})
	defer close(stop)
	go h.keepAlive(c, stop)
	for {
		var msg protocol.Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			return err
		}
		// Older agents handle targets serially. Responses also prove liveness
		// when queued requests delay their reading of a ping control frame.
		c.metricsMu.Lock()
		c.lastActiveAt = time.Now().UTC()
		c.metricsMu.Unlock()
		if err := c.conn.SetReadDeadline(time.Now().Add(h.pongWait)); err != nil {
			return err
		}
		if msg.Type == "response" {
			if ch, ok := c.pending.LoadAndDelete(msg.ID); ok {
				pending := ch.(pendingResponse)
				if pending.account && c.record != nil {
					c.record(pending.mapping, 0, uint64(len(msg.Body)))
				}
				pending.reply <- msg
			}
		}
		if msg.Type == "tcp-data" || msg.Type == "tcp-close" || msg.Type == "tcp-credit" {
			if value, ok := c.streams.Load(msg.ID); ok {
				stream := value.(*tcpConn)
				if msg.Type == "tcp-data" && len(msg.Body) <= protocol.MaxStreamBytes && c.record != nil {
					c.record("", 0, uint64(len(msg.Body)))
				}
				if msg.Type == "tcp-credit" {
					if !stream.addCredit(msg.Status) {
						stream.finish(errors.New("invalid stream credit"), true)
					}
				} else if msg.Type == "tcp-close" {
					stream.finish(io.EOF, false)
				} else if !stream.feed(msg.Body) {
					stream.finish(errors.New("remote stream buffer exceeded"), true)
				}
			}
		}
	}
}

func (h *Hub) keepAlive(c *Client, stop <-chan struct{}) {
	ticker := time.NewTicker(h.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Serialize with requests so a large in-flight data frame is not
			// mistaken for a failed ping. All writes still have a time limit.
			c.writeMu.Lock()
			select {
			case <-stop:
				c.writeMu.Unlock()
				return
			default:
			}
			now := time.Now()
			payload := "heartbeat:" + strconv.FormatInt(now.UnixNano(), 10)
			c.metricsMu.Lock()
			c.heartbeat, c.heartbeatAt = payload, now
			c.metricsMu.Unlock()
			err := c.conn.WriteControl(websocket.PingMessage, []byte(payload), now.Add(h.writeWait))
			c.writeMu.Unlock()
			if err != nil {
				_ = c.conn.Close()
				return
			}
		}
	}
}

// Ping measures the live tunnel without depending on an HTTP target or new agent commands.
func (h *Hub) Ping(ctx context.Context, deviceID string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	h.mu.RLock()
	c := h.clients[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return 0, ErrOffline
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, err
	}
	id := "probe:" + hex.EncodeToString(nonce[:])
	waiter := pingWaiter{started: time.Now(), result: make(chan pingResult, 1)}
	c.pings.Store(id, waiter)
	defer c.pings.Delete(id)
	deadline := time.Now().Add(h.writeWait)
	if expires, ok := ctx.Deadline(); ok && expires.Before(deadline) {
		deadline = expires
	}
	// WriteControl is safe beside the serialized data writer and honors this deadline.
	if err := c.conn.WriteControl(websocket.PingMessage, []byte(id), deadline); err != nil {
		return 0, err
	}
	select {
	case result := <-waiter.result:
		return result.latency, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (h *Hub) Request(ctx context.Context, deviceID string, msg protocol.Message, mapping ...string) (protocol.Message, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Message{}, err
	}
	h.mu.RLock()
	c := h.clients[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return protocol.Message{}, ErrOffline
	}
	return h.requestTo(ctx, c, msg, mapping...)
}

func (h *Hub) requestTo(ctx context.Context, c *Client, msg protocol.Message, mapping ...string) (protocol.Message, error) {
	response := make(chan protocol.Message, 1)
	pending := pendingResponse{reply: response, account: msg.Type == "request"}
	if len(mapping) > 0 {
		pending.mapping = mapping[0]
	}
	c.pending.Store(msg.ID, pending)
	defer c.pending.Delete(msg.ID)
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return protocol.Message{}, err
	}
	deadline := time.Now().Add(h.writeWait)
	if expires, ok := ctx.Deadline(); ok && expires.Before(deadline) {
		deadline = expires
	}
	err := c.conn.SetWriteDeadline(deadline)
	if err == nil {
		err = c.conn.WriteJSON(msg)
	}
	c.writeMu.Unlock()
	if err != nil {
		_ = c.conn.Close()
		return protocol.Message{}, err
	}
	if pending.account && c.record != nil {
		c.record(pending.mapping, uint64(len(msg.Body)), 0)
	}
	select {
	case result := <-response:
		return result, nil
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	}
}

type Status struct {
	DeviceID, Name, Version string
	Since                   time.Time
	CanUpdate               bool
	CanRemote               bool
	CanManageUpdate         bool
	LastActiveAt, LatencyAt time.Time
	LatencyMs               *int64
}

func (h *Hub) Statuses() map[string]Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]Status, len(h.clients))
	for id, c := range h.clients {
		c.metricsMu.Lock()
		status := Status{DeviceID: c.DeviceID, Name: c.Name, Version: c.Version, Since: c.Since, CanUpdate: c.CanUpdate, CanRemote: c.CanRemote, CanManageUpdate: c.CanManageUpdate, LastActiveAt: c.lastActiveAt, LatencyAt: c.latencyAt}
		if !c.latencyAt.IsZero() {
			ms := c.latency.Milliseconds()
			status.LatencyMs = &ms
		}
		c.metricsMu.Unlock()
		out[id] = status
	}
	return out
}

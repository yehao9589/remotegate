package hub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
)

var ErrOffline = errors.New("device is offline")

type Client struct {
	DeviceID string
	Name     string
	Version  string
	Since    time.Time
	conn     *websocket.Conn
	writeMu  sync.Mutex
	pending  sync.Map
}

type Hub struct {
	mu           sync.RWMutex
	clients      map[string]*Client
	pingInterval time.Duration
	pongWait     time.Duration
	writeWait    time.Duration
}

func New() *Hub {
	return &Hub{clients: make(map[string]*Client), pingInterval: 20 * time.Second, pongWait: 90 * time.Second, writeWait: 30 * time.Second}
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

func (h *Hub) Attach(deviceID, name, version string, conn *websocket.Conn) *Client {
	c := &Client{DeviceID: deviceID, Name: name, Version: version, Since: time.Now().UTC(), conn: conn}
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
			actual.(chan protocol.Message) <- protocol.Message{Type: "response", ID: key.(string), Error: "device disconnected"}
		}
		return true
	})
}

func (h *Hub) ReadLoop(c *Client) error {
	// Server-originated traffic keeps reverse proxies from closing idle tunnels.
	// WebSocket control frames work with the default pong handler in old agents.
	if err := c.conn.SetReadDeadline(time.Now().Add(h.pongWait)); err != nil {
		return err
	}
	c.conn.SetPongHandler(func(string) error {
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
		if err := c.conn.SetReadDeadline(time.Now().Add(h.pongWait)); err != nil {
			return err
		}
		if msg.Type == "response" {
			if ch, ok := c.pending.LoadAndDelete(msg.ID); ok {
				ch.(chan protocol.Message) <- msg
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
			err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(h.writeWait))
			c.writeMu.Unlock()
			if err != nil {
				_ = c.conn.Close()
				return
			}
		}
	}
}

func (h *Hub) Request(ctx context.Context, deviceID string, msg protocol.Message) (protocol.Message, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Message{}, err
	}
	h.mu.RLock()
	c := h.clients[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return protocol.Message{}, ErrOffline
	}
	response := make(chan protocol.Message, 1)
	c.pending.Store(msg.ID, response)
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
}

func (h *Hub) Statuses() map[string]Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]Status, len(h.clients))
	for id, c := range h.clients {
		out[id] = Status{c.DeviceID, c.Name, c.Version, c.Since}
	}
	return out
}

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
	mu      sync.RWMutex
	clients map[string]*Client
}

func New() *Hub { return &Hub{clients: make(map[string]*Client)} }

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
	for {
		var msg protocol.Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Type == "response" {
			if ch, ok := c.pending.LoadAndDelete(msg.ID); ok {
				ch.(chan protocol.Message) <- msg
			}
		}
	}
}

func (h *Hub) Request(ctx context.Context, deviceID string, msg protocol.Message) (protocol.Message, error) {
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
	err := c.conn.WriteJSON(msg)
	c.writeMu.Unlock()
	if err != nil {
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

package hub

import (
	"context"
	"github.com/local/remotegate/internal/protocol"
	"sync"
	"time"
)

type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) init() {
	m.once.Do(func() { m.token = make(chan struct{}, 1); m.token <- struct{}{} })
}
func (m *contextMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *contextMutex) Unlock() { m.token <- struct{}{} }
func (m *contextMutex) LockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
	}
	if err := ctx.Err(); err != nil {
		m.Unlock()
		return err
	}
	return nil
}
func (c *Client) cancelRequest(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c.writeMu.LockContext(ctx) != nil {
		return
	}
	defer c.writeMu.Unlock()
	deadline, _ := ctx.Deadline()
	if c.conn.SetWriteDeadline(deadline) != nil {
		return
	}
	if c.conn.WriteJSON(protocol.Message{Type: "request-cancel", ID: id}) != nil {
		_ = c.conn.Close()
	}
}

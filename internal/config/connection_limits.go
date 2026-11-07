package config

import (
	"errors"
	"os"

	"github.com/local/remotegate/internal/protocol"
)

// ConnectionLimits applies to all mappings belonging to one device. Ordinary
// requests may borrow idle WebSocket capacity, but WebSockets cannot borrow the
// HTTP reserve.
type ConnectionLimits struct {
	WebSockets  int `json:"webSockets"`
	HTTPReserve int `json:"httpReserve"`
}

func DefaultConnectionLimits() ConnectionLimits {
	return ConnectionLimits{WebSockets: protocol.DefaultWebSocketStreams, HTTPReserve: protocol.MaxHTTPStreams - protocol.DefaultWebSocketStreams}
}

func (l ConnectionLimits) WithDefaults() ConnectionLimits {
	if l == (ConnectionLimits{}) {
		return DefaultConnectionLimits()
	}
	return l
}

func (l ConnectionLimits) Validate() error {
	if l.WebSockets < 1 || l.WebSockets > protocol.MaxHTTPStreamCapacity-4 || l.HTTPReserve < 4 || l.HTTPReserve > 64 || l.WebSockets+l.HTTPReserve > protocol.MaxHTTPStreamCapacity {
		return errors.New("实时连接需为 1–124 条，普通网页预留需为 4–64 条，合计最多 128 条")
	}
	return nil
}

func (s *Store) DeviceConnectionLimits(id string) ConnectionLimits {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.state.Devices {
		if d.ID == id {
			return d.ConnectionLimits.WithDefaults()
		}
	}
	return DefaultConnectionLimits()
}

func (s *Store) SetDeviceConnectionLimits(id string, limits ConnectionLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, before := range s.state.Devices {
		if before.ID != id {
			continue
		}
		s.state.Devices[i].ConnectionLimits = limits
		if err := s.saveLocked(); err != nil {
			s.state.Devices[i] = before
			return err
		}
		return nil
	}
	return os.ErrNotExist
}

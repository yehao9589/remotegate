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
	// Zero retains safe defaults for older settings; -1 explicitly disables it.
	IdleMinutes       int `json:"idleMinutes,omitempty"`
	BackgroundMinutes int `json:"backgroundMinutes,omitempty"`
}

type ConnectionLimitSettings struct {
	Defaults     ConnectionLimits `json:"defaults"`
	DeviceCount  int              `json:"deviceCount"`
	AppliedCount int              `json:"appliedCount"`
}

func (s *Store) ConnectionLimitDefaults() ConnectionLimitSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ConnectionLimitSettings{Defaults: s.state.DefaultConnectionLimits.WithDefaults(), DeviceCount: len(s.state.Devices)}
}

// Save defaults and optional bulk replacement as one write. Existing devices
// retain their independent limits unless the administrator explicitly applies
// the defaults to all of them. Pending enrollments join the bulk change only
// after persistence succeeds.
func (s *Store) SetConnectionLimitDefaults(limits ConnectionLimits, applyToAll bool) (ConnectionLimitSettings, error) {
	if err := limits.Validate(); err != nil {
		return ConnectionLimitSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	beforeDefaults, beforeDevices := s.state.DefaultConnectionLimits, s.state.Devices
	s.state.DefaultConnectionLimits = limits
	if applyToAll {
		s.state.Devices = append([]Device(nil), beforeDevices...)
		for i := range s.state.Devices {
			s.state.Devices[i].ConnectionLimits = limits
		}
	}
	if err := s.saveLocked(); err != nil {
		s.state.DefaultConnectionLimits, s.state.Devices = beforeDefaults, beforeDevices
		return ConnectionLimitSettings{}, err
	}
	result := ConnectionLimitSettings{Defaults: limits, DeviceCount: len(s.state.Devices)}
	if applyToAll {
		result.AppliedCount = len(s.state.Devices)
		for id, d := range s.pending {
			d.ConnectionLimits = limits
			s.pending[id] = d
		}
	}
	return result, nil
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
	if l.IdleMinutes < -1 || l.IdleMinutes > 240 || l.BackgroundMinutes < -1 || l.BackgroundMinutes > 60 {
		return errors.New("无操作暂停时间最多 240 分钟，后台暂停时间最多 60 分钟；-1 表示关闭")
	}
	if l.WebSockets < 1 || l.WebSockets > protocol.MaxHTTPStreamCapacity-4 || l.HTTPReserve < 4 || l.HTTPReserve > 64 || l.WebSockets+l.HTTPReserve > protocol.MaxHTTPStreamCapacity {
		return errors.New("实时连接需为 1–124 条，普通网页预留需为 4–64 条，合计最多 128 条")
	}
	return nil
}

func (l ConnectionLimits) ActivityTimeouts() (idle, background int) {
	idle, background = l.IdleMinutes, l.BackgroundMinutes
	if idle == 0 {
		idle = 15
	}
	if background == 0 {
		background = 1
	}
	if idle < 0 {
		idle = 0
	}
	if background < 0 {
		background = 0
	}
	return
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

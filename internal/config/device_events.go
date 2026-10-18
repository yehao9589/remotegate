package config

import "time"

const ConnectionEventLimit = 100

// Times are observations by this server, not the router's wall clock.
type ConnectionEvent struct {
	State       string    `json:"state"`
	At          time.Time `json:"at"`
	ConnectedAt time.Time `json:"connectedAt,omitempty"`
	Reason      string    `json:"reason"`
}

// Caller holds s.mu. Copy before appending so failed saves and old snapshots
// cannot mutate the previous event buffer.
func (s *Store) saveConnectionLocked(index int, next Device, event ConnectionEvent) error {
	before := s.state.Devices[index]
	if s.state.ConnectionEvents == nil {
		s.state.ConnectionEvents = make(map[string][]ConnectionEvent)
	}
	events, existed := s.state.ConnectionEvents[next.ID]
	start := 0
	if len(events) >= ConnectionEventLimit {
		start = len(events) - ConnectionEventLimit + 1
	}
	updated := append([]ConnectionEvent(nil), events[start:]...)
	s.state.ConnectionEvents[next.ID] = append(updated, event)
	s.state.Devices[index] = next
	if err := s.saveLocked(); err != nil {
		s.state.Devices[index] = before
		if existed {
			s.state.ConnectionEvents[next.ID] = events
		} else {
			delete(s.state.ConnectionEvents, next.ID)
		}
		return err
	}
	return nil
}

// DeviceConnectionEvents returns newest first without exposing credentials.
func (s *Store) DeviceConnectionEvents(id string) ([]ConnectionEvent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.state.Devices {
		if d.ID != id {
			continue
		}
		events := s.state.ConnectionEvents[id]
		out := make([]ConnectionEvent, len(events))
		for i, event := range events {
			out[len(events)-1-i] = event
		}
		return out, true
	}
	return nil, false
}

// Startup cannot know exactly when a previous process lost its connections.
// Record when the loss was discovered and label it explicitly.
func (s *Store) RecoverConnections(at time.Time) error {
	for _, d := range s.Snapshot().Devices {
		if !d.LastConnectedAt.IsZero() && d.LastDisconnectedAt.Before(d.LastConnectedAt) {
			if err := s.RecordDisconnect(d.ID, d.LastConnectedAt, at, "server_restart"); err != nil {
				return err
			}
		}
	}
	return nil
}

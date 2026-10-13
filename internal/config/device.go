package config

import (
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Info is reported at connection time, never inferred from an installed package.
type DeviceInfo struct {
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Interface string `json:"interface,omitempty"`
	PeerIP    string `json:"peerIP,omitempty"`
}

func (info DeviceInfo) Validate() error {
	for _, value := range []string{info.OS, info.Arch, info.Hostname, info.Interface, info.PeerIP} {
		if !utf8.ValidString(value) || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid device information")
		}
	}
	return nil
}

// UpdateDevice accepts partial edits so older consoles can still rename devices.
func (s *Store) UpdateDevice(id string, name, note *string, pinned *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, before := range s.state.Devices {
		if before.ID != id {
			continue
		}
		next := before
		if name != nil {
			next.Name = strings.TrimSpace(*name)
			if next.Name == "" || utf8.RuneCountInString(next.Name) > 100 {
				return errors.New("设备名称需为 1–100 个字符")
			}
		}
		if note != nil {
			next.Note = strings.TrimSpace(*note)
			if utf8.RuneCountInString(next.Note) > 200 {
				return errors.New("设备备注最多 200 个字符")
			}
		}
		if pinned != nil {
			next.Pinned = *pinned
		}
		s.state.Devices[i] = next
		if err := s.saveLocked(); err != nil {
			s.state.Devices[i] = before
			return err
		}
		return nil
	}
	return os.ErrNotExist
}

// Connection times are saved only on lifecycle changes, not on every heartbeat.
func (s *Store) RecordConnection(id string, at time.Time, info DeviceInfo) error {
	if err := info.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, before := range s.state.Devices {
		if before.ID != id {
			continue
		}
		if at.Before(before.LastConnectedAt) {
			return nil
		}
		s.state.Devices[i].LastConnectedAt = at.UTC()
		s.state.Devices[i].Info = info
		if err := s.saveLocked(); err != nil {
			s.state.Devices[i] = before
			return err
		}
		return nil
	}
	return os.ErrNotExist
}

func (s *Store) RecordDisconnect(id string, connectedAt, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, before := range s.state.Devices {
		if before.ID != id {
			continue
		}
		// A replaced session must never overwrite the new connection's history.
		if !before.LastConnectedAt.Equal(connectedAt) || !at.After(before.LastDisconnectedAt) {
			return nil
		}
		s.state.Devices[i].LastDisconnectedAt = at.UTC()
		if err := s.saveLocked(); err != nil {
			s.state.Devices[i] = before
			return err
		}
		return nil
	}
	return nil // The device may have been deleted while its tunnel was closing.
}

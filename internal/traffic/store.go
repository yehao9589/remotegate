// Package traffic records cumulative tunnel payload sizes, independently of configuration.
package traffic

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Counter struct {
	UploadBytes   uint64    `json:"uploadBytes"`
	DownloadBytes uint64    `json:"downloadBytes"`
	UpdatedAt     time.Time `json:"updatedAt,omitempty"`
}

type Snapshot struct {
	Version  int                `json:"version"`
	Since    time.Time          `json:"since"`
	Devices  map[string]Counter `json:"devices"`
	Mappings map[string]Counter `json:"mappings"`
}

type Store struct {
	mu, flushMu     sync.Mutex
	state           Snapshot
	path            string
	revision, saved uint64
	stop, done      chan struct{}
	once            sync.Once
	closeErr        error
}

func New() *Store {
	return &Store{state: Snapshot{Version: 1, Since: time.Now().UTC(), Devices: map[string]Counter{}, Mappings: map[string]Counter{}}, revision: 1}
}

// Open keeps the same start time even when the first period has no traffic.
func Open(path string) (*Store, error) {
	s := New()
	s.path = path
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) > 32<<20 {
			return nil, errors.New("traffic file exceeds size limit")
		}
		if err = json.Unmarshal(raw, &s.state); err != nil || s.state.Version != 1 || s.state.Since.IsZero() {
			return nil, errors.New("invalid traffic statistics file; original data preserved")
		}
		if s.state.Devices == nil {
			s.state.Devices = map[string]Counter{}
		}
		if s.state.Mappings == nil {
			s.state.Mappings = map[string]Counter{}
		}
		s.saved = s.revision
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = s.Flush(); err != nil {
		return nil, err
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.Flush(); err != nil {
					log.Printf("流量统计保存失败，将自动重试：%v", err)
				}
			case <-s.stop:
				s.closeErr = s.Flush()
				return
			}
		}
	}()
	return s, nil
}

func plus(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

// Add is called only for actual payload transfers, never for heartbeats or control messages.
// Upload means towards the device; download means from the device.
func (s *Store) Add(device, mapping string, upload, download uint64) {
	if device == "" || upload == 0 && download == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	update := func(c Counter) Counter {
		c.UploadBytes, c.DownloadBytes = plus(c.UploadBytes, upload), plus(c.DownloadBytes, download)
		c.UpdatedAt = time.Now().UTC()
		return c
	}
	s.state.Devices[device] = update(s.state.Devices[device])
	if mapping != "" {
		s.state.Mappings[mapping] = update(s.state.Mappings[mapping])
	}
	s.revision++
}

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyLocked()
}

func (s *Store) copyLocked() Snapshot {
	out := Snapshot{Version: s.state.Version, Since: s.state.Since, Devices: make(map[string]Counter, len(s.state.Devices)), Mappings: make(map[string]Counter, len(s.state.Mappings))}
	for id, c := range s.state.Devices {
		out.Devices[id] = c
	}
	for id, c := range s.state.Mappings {
		out.Mappings[id] = c
	}
	return out
}

// Flush snapshots without holding up transfers while the file is written. A failed
// save leaves the counters dirty so the next periodic save retries the same data.
func (s *Store) Flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	if s.path == "" || s.saved == s.revision {
		s.mu.Unlock()
		return nil
	}
	snapshot, revision := s.copyLocked(), s.revision
	s.mu.Unlock()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".traffic-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return fmt.Errorf("save traffic: %w", err)
	}
	s.mu.Lock()
	s.saved = revision
	s.mu.Unlock()
	return nil
}

func (s *Store) Close() error {
	if s.stop == nil {
		return s.Flush()
	}
	s.once.Do(func() { close(s.stop); <-s.done })
	return s.closeErr
}

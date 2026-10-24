package config

import (
	"sort"
	"time"
)

type Alert struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	DeviceID   string     `json:"deviceId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	Read       bool       `json:"read"`
}

// Reconciliation deduplicates ongoing incidents, retains resolutions, and
// starts a new history entry if the same condition recurs after recovery.
func (s *Store) ReconcileAlerts(conditions []Alert, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conditions = append([]Alert(nil), conditions...)
	sort.SliceStable(conditions, func(i, j int) bool { return conditions[i].Key < conditions[j].Key })
	unique := conditions[:0]
	for _, c := range conditions {
		if len(unique) == 0 || unique[len(unique)-1].Key != c.Key {
			unique = append(unique, c)
		}
	}
	conditions = unique
	if len(conditions) > 200 {
		conditions = conditions[:200]
	}
	old := s.state.Alerts
	next := append([]Alert(nil), old...)
	active := map[string]int{}
	for i, a := range next {
		if a.ResolvedAt == nil {
			active[a.Key] = i
		}
	}
	wanted := map[string]bool{}
	changed := false
	for _, c := range conditions {
		wanted[c.Key] = true
		if i, ok := active[c.Key]; ok {
			if next[i].Title != c.Title || next[i].Detail != c.Detail {
				if next[i].Title != c.Title {
					next[i].Read = false
				}
				next[i].Title = c.Title
				next[i].Detail = c.Detail
				changed = true
			}
			continue
		}
		id, err := randomToken(12)
		if err != nil {
			return err
		}
		c.ID = id
		c.CreatedAt = now.UTC()
		c.Read = false
		c.ResolvedAt = nil
		next = append(next, c)
		changed = true
	}
	for key, i := range active {
		if !wanted[key] {
			resolved := now.UTC()
			next[i].ResolvedAt = &resolved
			next[i].Read = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(next) > 200 {
		// Keep ongoing incidents before old resolved history, so a busy system
		// doesn't repeatedly recreate an incident evicted on the previous pass.
		activeCount := 0
		for _, a := range next {
			if a.ResolvedAt == nil {
				activeCount++
			}
		}
		budget := 200 - activeCount
		kept := []Alert{}
		for i := len(next) - 1; i >= 0; i-- {
			if next[i].ResolvedAt == nil {
				kept = append(kept, next[i])
			} else if budget > 0 {
				kept = append(kept, next[i])
				budget--
			}
		}
		for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
			kept[i], kept[j] = kept[j], kept[i]
		}
		next = kept
	}
	s.state.Alerts = next
	if err := s.saveLocked(); err != nil {
		s.state.Alerts = old
		return err
	}
	return nil
}

func (s *Store) Alerts() []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Alert, len(s.state.Alerts))
	for i, a := range s.state.Alerts {
		if a.ResolvedAt != nil {
			copy := *a.ResolvedAt
			a.ResolvedAt = &copy
		}
		out[len(out)-1-i] = a
	}
	return out
}

func (s *Store) ReadAlerts() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.state.Alerts
	next := append([]Alert(nil), old...)
	for i := range next {
		next[i].Read = true
	}
	s.state.Alerts = next
	if err := s.saveLocked(); err != nil {
		s.state.Alerts = old
		return err
	}
	return nil
}

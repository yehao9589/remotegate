package traffic

import (
	"sort"
	"time"
)

type Point struct {
	Date          string `json:"date"`
	UploadBytes   uint64 `json:"uploadBytes"`
	DownloadBytes uint64 `json:"downloadBytes"`
	Observed      bool   `json:"observed"`
}
type Trend struct {
	Since    time.Time `json:"since"`
	Timezone string    `json:"timezone"`
	Period   string    `json:"period"`
	Points   []Point   `json:"points"`
	Ranking  []Rank    `json:"ranking"`
}
type Rank struct {
	ID            string `json:"id"`
	UploadBytes   uint64 `json:"uploadBytes"`
	DownloadBytes uint64 `json:"downloadBytes"`
}

// Series includes only the requested current entity IDs. Cumulative totals from
// older versions are deliberately not assigned to invented historical dates.
func (s *Store) Series(scope string, ids []string, period string, now time.Time) Trend {
	s.mu.Lock()
	defer s.mu.Unlock()
	now = now.UTC()
	count := 30
	if period == "month" {
		count = 12
	} else {
		period = "day"
	}
	out := Trend{Since: s.state.HistorySince, Timezone: "UTC", Period: period, Points: []Point{}}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	first := dayStart.AddDate(0, 0, -count+1)
	if period == "month" {
		first = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -count+1, 0)
	}
	index := map[string]int{}
	for i := 0; i < count; i++ {
		start := first.AddDate(0, 0, i)
		end := start.AddDate(0, 0, 1)
		key := start.Format("2006-01-02")
		if period == "month" {
			start = first.AddDate(0, i, 0)
			end = start.AddDate(0, 1, 0)
			key = start.Format("2006-01")
		}
		index[key] = i
		out.Points = append(out.Points, Point{Date: key, Observed: end.After(s.state.HistorySince)})
	}
	ranks := map[string]Counter{}
	for date, day := range s.state.Days {
		key := date
		if period == "month" && len(date) >= 7 {
			key = date[:7]
		}
		i, ok := index[key]
		if !ok || date > dayStart.Format("2006-01-02") {
			continue
		}
		counters := day.Devices
		if scope == "mappings" {
			counters = day.Mappings
		}
		for id, c := range counters {
			if allowed[id] {
				out.Points[i].UploadBytes = plus(out.Points[i].UploadBytes, c.UploadBytes)
				out.Points[i].DownloadBytes = plus(out.Points[i].DownloadBytes, c.DownloadBytes)
				r := ranks[id]
				r.UploadBytes = plus(r.UploadBytes, c.UploadBytes)
				r.DownloadBytes = plus(r.DownloadBytes, c.DownloadBytes)
				ranks[id] = r
			}
		}
	}
	out.Ranking = []Rank{}
	for id, c := range ranks {
		out.Ranking = append(out.Ranking, Rank{id, c.UploadBytes, c.DownloadBytes})
	}
	sort.Slice(out.Ranking, func(i, j int) bool {
		left, right := plus(out.Ranking[i].UploadBytes, out.Ranking[i].DownloadBytes), plus(out.Ranking[j].UploadBytes, out.Ranking[j].DownloadBytes)
		if left == right {
			return out.Ranking[i].ID < out.Ranking[j].ID
		}
		return left > right
	})
	if len(out.Ranking) > 10 {
		out.Ranking = out.Ranking[:10]
	}
	return out
}

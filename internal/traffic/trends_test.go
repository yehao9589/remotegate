package traffic

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrendsDailyMonthlyAttributionAndPersistence(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "traffic.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.state.HistorySince = now.AddDate(0, 0, -10)
	s.addAt("router", "map", 10, 20, now.AddDate(0, 0, -1))
	s.addAt("router", "", 30, 40, now) // SSH belongs to the device only.
	s.addAt("removed", "removed", 999, 999, now)
	daily := s.Series("devices", []string{"router"}, "day", now)
	if len(daily.Points) != 30 || daily.Points[0].Observed || !daily.Points[29].Observed || daily.Points[28].UploadBytes != 10 || daily.Points[29].DownloadBytes != 40 || len(daily.Ranking) != 1 {
		t.Fatal(daily)
	}
	month := s.Series("mappings", []string{"map"}, "month", now)
	if len(month.Points) != 12 || month.Points[11].DownloadBytes != 20 || month.Ranking[0].UploadBytes != 10 {
		t.Fatal(month)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Series("devices", []string{"router"}, "day", now).Points[29].UploadBytes != 30 {
		t.Fatal("history lost on restart")
	}
	copy := restored.Series("devices", []string{"router"}, "day", now)
	copy.Points[29].UploadBytes = 9999
	if restored.Series("devices", []string{"router"}, "day", now).Points[29].UploadBytes != 30 {
		t.Fatal("response aliases state")
	}
}

func TestTrendUpgradePreservesTotalsWithoutInventingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	os.WriteFile(path, []byte(`{"version":1,"since":"2026-01-01T00:00:00Z","devices":{"router":{"uploadBytes":12345}},"mappings":{}}`), 0600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	since := s.Series("devices", []string{"router"}, "day", time.Now()).Since
	if since.IsZero() || s.Snapshot().Devices["router"].UploadBytes != 12345 {
		t.Fatal("migration lost counters")
	}
	for _, point := range s.Series("devices", []string{"router"}, "day", time.Now()).Points {
		if point.UploadBytes != 0 {
			t.Fatal("fabricated past traffic")
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Series("devices", nil, "day", time.Now()).Since.Equal(since) {
		t.Fatal("migration start time changed")
	}
}

func TestTrendRetentionAndUTCBoundaries(t *testing.T) {
	s := New()
	now := time.Date(2026, 10, 22, 0, 30, 0, 0, time.UTC)
	s.state.HistorySince = now.AddDate(-2, 0, 0)
	s.addAt("r", "m", 99, 99, now.AddDate(0, 0, -400))
	s.addAt("r", "m", 1, 2, now.In(time.FixedZone("local", 8*3600)))
	if len(s.state.Days) != 1 {
		t.Fatal("old daily buckets retained")
	}
	daily := s.Series("devices", []string{"r"}, "day", now)
	if daily.Timezone != "UTC" || daily.Points[29].Date != "2026-10-22" || daily.Points[29].UploadBytes != 1 {
		t.Fatal(daily)
	}
	if s.Snapshot().Devices["r"].UploadBytes != 100 {
		t.Fatal("retention deleted cumulative traffic")
	}
}

package traffic

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDeviceAndMappingAttributionAndSnapshotIsolation(t *testing.T) {
	s := New()
	s.Add("one", "a", 3, 7)
	s.Add("one", "b", 11, 13)
	s.Add("one", "", 23, 29)
	s.Add("two", "c", 17, 19)
	s.Add("one", "empty", 0, 0)
	s.Add("", "ignored", 100, 100)
	got := s.Snapshot()
	if got.Devices["one"].UploadBytes != 37 || got.Devices["one"].DownloadBytes != 49 || got.Mappings["a"].DownloadBytes != 7 || got.Mappings["b"].UploadBytes != 11 || len(got.Mappings) != 3 || len(got.Devices) != 2 {
		t.Fatal(got)
	}
	delete(got.Mappings, "a")
	delete(got.Devices, "one")
	if s.Snapshot().Mappings["a"].UploadBytes != 3 || s.Snapshot().Devices["one"].DownloadBytes != 49 {
		t.Fatal("snapshot mutates stored counters")
	}
}

func TestConcurrentTransfersAndCounterOverflow(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.Add("one", "a", 3, 7)
			}
		}()
	}
	wg.Wait()
	got := s.Snapshot()
	if got.Devices["one"].UploadBytes != 96000 || got.Mappings["a"].DownloadBytes != 224000 {
		t.Fatal(got)
	}
	s.Add("one", "a", math.MaxUint64, math.MaxUint64)
	if s.Snapshot().Devices["one"].UploadBytes != math.MaxUint64 || s.Snapshot().Mappings["a"].DownloadBytes != math.MaxUint64 {
		t.Fatal("overflow reset cumulative data")
	}
}

func TestCloseSavesLatestTransfersAndReopenPreservesStartTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	since := s.Snapshot().Since
	s.Add("router", "mapping", 123, 456)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Snapshot().Since.Equal(since) || s.Snapshot().Mappings["mapping"].DownloadBytes != 456 {
		t.Fatal("restart lost counters or start time")
	}
	s.Add("router", "mapping", 2, 3)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	var saved Snapshot
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &saved) != nil || saved.Devices["router"].UploadBytes != 125 {
		t.Fatal("new transfers were not saved")
	}
}

func TestFailedSaveRetriesWithoutDiscardingCounters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	path := filepath.Join(dir, "traffic.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = os.Rename(dir, dir+".backup"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dir, []byte("blocked directory"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Add("router", "mapping", 41, 43)
	if s.Flush() == nil {
		t.Fatal("write failure hidden")
	}
	if err = os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dir+".backup", dir); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var saved Snapshot
	if json.Unmarshal(raw, &saved) != nil || saved.Mappings["mapping"].DownloadBytes != 43 {
		t.Fatal("failed save dropped transfers")
	}
}

func TestInvalidStatisticsAreNotSilentlyOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	original := []byte(`{"version":99,"since":"2026-10-18T00:00:00Z"}`)
	os.WriteFile(path, original, 0600)
	if _, err := Open(path); err == nil {
		t.Fatal("invalid data accepted")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(original) {
		t.Fatal("invalid statistics were overwritten")
	}
}

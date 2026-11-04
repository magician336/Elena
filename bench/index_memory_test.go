package bench

import (
	"path/filepath"
	"testing"

	"elena"
)

// TestComputeIndexMemoryDerivesBytesPerEntry pins the figure's basis:
// Stats's index estimate divided by its live-entry count, the same
// bytes-per-entry unit a competitor's index cost is reported in.
func TestComputeIndexMemoryDerivesBytesPerEntry(t *testing.T) {
	cases := []struct {
		name        string
		stats       elena.Stats
		wantPerTick float64
	}{
		{"exact division", elena.Stats{LiveEntries: 100, IndexMemoryBytes: 5000}, 50},
		{"fractional result", elena.Stats{LiveEntries: 3, IndexMemoryBytes: 100}, float64(100) / 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeIndexMemory(tc.stats)
			if err != nil {
				t.Fatalf("ComputeIndexMemory: %v", err)
			}
			if got.LiveEntries != tc.stats.LiveEntries || got.IndexMemoryBytes != tc.stats.IndexMemoryBytes {
				t.Fatalf("result carries (%d, %d), want the Stats figures unchanged (%d, %d)",
					got.LiveEntries, got.IndexMemoryBytes, tc.stats.LiveEntries, tc.stats.IndexMemoryBytes)
			}
			if got.BytesPerEntry != tc.wantPerTick {
				t.Fatalf("BytesPerEntry = %g, want %g", got.BytesPerEntry, tc.wantPerTick)
			}
		})
	}
}

// TestComputeIndexMemoryRejectsEmptyIndex pins the division's guard: a
// store with no live entries has no per-entry figure, an error rather
// than a zero that reads like a measurement.
func TestComputeIndexMemoryRejectsEmptyIndex(t *testing.T) {
	if _, err := ComputeIndexMemory(elena.Stats{}); err == nil {
		t.Fatal("ComputeIndexMemory of no live entries succeeded, want error")
	}
}

// TestRunIndexMemoryMicro drives the scenario end to end at micro
// scale: a seeded dataset yields the per-entry figure off the public
// Stats, with the live-entry count at least one index entry per
// appended event.
func TestRunIndexMemoryMicro(t *testing.T) {
	cfg := IndexMemoryConfig{Sessions: 5, EventsPerSession: 4, ValueSize: 128}
	got, err := RunIndexMemory(filepath.Join(t.TempDir(), "data"), cfg)
	if err != nil {
		t.Fatalf("RunIndexMemory: %v", err)
	}
	if got.LiveEntries < 20 { // 5 sessions x 4 events, metadata aside
		t.Fatalf("LiveEntries = %d, want at least 20", got.LiveEntries)
	}
	if got.IndexMemoryBytes == 0 {
		t.Fatal("IndexMemoryBytes = 0 over a seeded index, want a positive estimate")
	}
	if got.BytesPerEntry <= 0 {
		t.Fatalf("BytesPerEntry = %g, want positive", got.BytesPerEntry)
	}
}

// TestRunIndexMemoryRejectsReuse pins the fresh-directory contract:
// a second run over the same dataset directory fails instead of
// stacking figures on stale data.
func TestRunIndexMemoryRejectsReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	cfg := IndexMemoryConfig{Sessions: 2, EventsPerSession: 2, ValueSize: 64}
	if _, err := RunIndexMemory(dir, cfg); err != nil {
		t.Fatalf("first RunIndexMemory: %v", err)
	}
	if _, err := RunIndexMemory(dir, cfg); err == nil {
		t.Fatal("second RunIndexMemory over the same dir succeeded, want failure")
	}
}

// TestRunIndexMemoryValidatesConfig pins the declared shape's guards.
func TestRunIndexMemoryValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  IndexMemoryConfig
	}{
		{"zero sessions", IndexMemoryConfig{Sessions: 0, EventsPerSession: 1, ValueSize: 1}},
		{"zero events", IndexMemoryConfig{Sessions: 1, EventsPerSession: 0, ValueSize: 1}},
		{"zero value size", IndexMemoryConfig{Sessions: 1, EventsPerSession: 1, ValueSize: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunIndexMemory(filepath.Join(t.TempDir(), "data"), tc.cfg); err == nil {
				t.Fatal("RunIndexMemory accepted an invalid config, want error")
			}
		})
	}
}

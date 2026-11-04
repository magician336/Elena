package bench

import (
	"path/filepath"
	"testing"

	"elena"
)

// TestWriteAmplificationReadingOfMapsStats pins the reading's basis:
// the logical/flushed byte pair and their ratio copied off Stats, the
// store's own write-amplification accounting.
func TestWriteAmplificationReadingOfMapsStats(t *testing.T) {
	stats := elena.Stats{BytesWritten: 1000, BytesFlushed: 2500, WriteAmplification: 2.5}
	got := WriteAmplificationReadingOf(stats)
	if got.LogicalBytes != 1000 || got.FlushedBytes != 2500 || got.Ratio != 2.5 {
		t.Fatalf("reading = %+v, want the Stats figures (1000, 2500, 2.5) unchanged", got)
	}
}

// TestWriteAmplificationReadingOfZeroLogical pins the empty-store
// reading: Stats reports ratio 0 while no logical bytes exist, and the
// reading forwards that pair rather than inventing a ratio.
func TestWriteAmplificationReadingOfZeroLogical(t *testing.T) {
	got := WriteAmplificationReadingOf(elena.Stats{})
	if got.LogicalBytes != 0 || got.FlushedBytes != 0 || got.Ratio != 0 {
		t.Fatalf("reading of an empty store = %+v, want all zeros", got)
	}
}

// TestRunWriteAmplificationMicro drives the scenario end to end at
// micro scale and pins the declared accounting: one explicit merge
// rewrites the live dataset, the rewrite lands in flushed bytes, and
// the ratio steps up while the logical byte count stands still.
func TestRunWriteAmplificationMicro(t *testing.T) {
	cfg := WriteAmplificationConfig{Sessions: 4, EventsPerSession: 5, ValueSize: 256}
	got, err := RunWriteAmplification(filepath.Join(t.TempDir(), "data"), cfg)
	if err != nil {
		t.Fatalf("RunWriteAmplification: %v", err)
	}
	if got.BeforeMerge.LogicalBytes == 0 || got.BeforeMerge.FlushedBytes == 0 {
		t.Fatalf("before-merge reading = %+v, want positive byte counts", got.BeforeMerge)
	}
	if got.AfterMerge.LogicalBytes != got.BeforeMerge.LogicalBytes {
		t.Fatalf("logical bytes moved across the merge: before %d, after %d, want unchanged",
			got.BeforeMerge.LogicalBytes, got.AfterMerge.LogicalBytes)
	}
	if got.MergeFlushedBytes == 0 {
		t.Fatal("merge flushed 0 bytes over a live dataset, want the rewrite counted as flushed")
	}
	if got.AfterMerge.FlushedBytes != got.BeforeMerge.FlushedBytes+got.MergeFlushedBytes {
		t.Fatalf("flushed bytes = %d, want before %d + merge %d",
			got.AfterMerge.FlushedBytes, got.BeforeMerge.FlushedBytes, got.MergeFlushedBytes)
	}
	if got.AfterMerge.Ratio <= got.BeforeMerge.Ratio {
		t.Fatalf("ratio after merge = %g, want above the before-merge %g", got.AfterMerge.Ratio, got.BeforeMerge.Ratio)
	}
}

// TestRunWriteAmplificationRejectsReuse pins the fresh-directory
// contract: a second run over the same dataset directory fails.
func TestRunWriteAmplificationRejectsReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	cfg := WriteAmplificationConfig{Sessions: 2, EventsPerSession: 2, ValueSize: 64}
	if _, err := RunWriteAmplification(dir, cfg); err != nil {
		t.Fatalf("first RunWriteAmplification: %v", err)
	}
	if _, err := RunWriteAmplification(dir, cfg); err == nil {
		t.Fatal("second RunWriteAmplification over the same dir succeeded, want failure")
	}
}

// TestRunWriteAmplificationValidatesConfig pins the declared shape's
// guards.
func TestRunWriteAmplificationValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  WriteAmplificationConfig
	}{
		{"zero sessions", WriteAmplificationConfig{Sessions: 0, EventsPerSession: 1, ValueSize: 1}},
		{"zero events", WriteAmplificationConfig{Sessions: 1, EventsPerSession: 0, ValueSize: 1}},
		{"zero value size", WriteAmplificationConfig{Sessions: 1, EventsPerSession: 1, ValueSize: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunWriteAmplification(filepath.Join(t.TempDir(), "data"), tc.cfg); err == nil {
				t.Fatal("RunWriteAmplification accepted an invalid config, want error")
			}
		})
	}
}

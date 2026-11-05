package bench

import (
	"fmt"
	"os"

	"github.com/magician336/elena"
)

// NutsDBIndexMemoryReference is the competitor figure the per-entry
// index memory number is read against: nutsdb's publicly stated
// in-memory index cost of 145 bytes per entry, reduced to 50 by its
// later index work. The figure is vendor-reported — published by the
// nutsdb authors, not measured by this harness — and is quoted in the
// same unit as the measured number: bytes per entry.
const NutsDBIndexMemoryReference = "nutsdb 145→50 B/entry 【vendor-reported】"

// IndexMemoryConfig declares the dataset the per-entry index memory
// figure is measured over: EventsPerSession events of ValueSize bytes
// appended to each of Sessions trajectories. All fields must be
// positive.
type IndexMemoryConfig struct {
	Sessions         int
	EventsPerSession int
	ValueSize        int
}

// IndexMemoryResult is the per-entry index memory figure and the two
// Stats observables it divides — one number in the unit a competitor's
// index cost is reported in (bytes per entry), plus the raw pair so the
// division stays inspectable.
type IndexMemoryResult struct {
	// LiveEntries is Stats().LiveEntries at the reading.
	LiveEntries uint64 `json:"live_entries"`

	// IndexMemoryBytes is Stats().IndexMemoryBytes at the reading: the
	// engine's estimate of the in-memory index footprint.
	IndexMemoryBytes uint64 `json:"index_memory_bytes"`

	// BytesPerEntry is IndexMemoryBytes / LiveEntries.
	BytesPerEntry float64 `json:"bytes_per_entry"`
}

// ComputeIndexMemory derives the per-entry figure from one Stats
// reading: the index estimate divided by the live-entry count. A store
// with no live entries has no per-entry figure and reports an error
// rather than a zero that would read like a measurement.
func ComputeIndexMemory(stats elena.Stats) (IndexMemoryResult, error) {
	if stats.LiveEntries == 0 {
		return IndexMemoryResult{}, fmt.Errorf("index memory: no live entries to divide by")
	}
	return IndexMemoryResult{
		LiveEntries:      stats.LiveEntries,
		IndexMemoryBytes: stats.IndexMemoryBytes,
		BytesPerEntry:    float64(stats.IndexMemoryBytes) / float64(stats.LiveEntries),
	}, nil
}

// RunIndexMemory seeds the declared dataset into a fresh store under
// dir — which must not exist; the store creates it — and reads the
// per-entry index memory figure off the public Stats. The dataset is
// pure appends, so the figure it yields is the cost of a live index:
// dead data's index bytes are reclaimed by a merge, which this
// scenario deliberately does not run.
func RunIndexMemory(dir string, cfg IndexMemoryConfig) (IndexMemoryResult, error) {
	if cfg.Sessions <= 0 {
		return IndexMemoryResult{}, fmt.Errorf("index memory scenario: sessions %d must be positive", cfg.Sessions)
	}
	if cfg.EventsPerSession <= 0 {
		return IndexMemoryResult{}, fmt.Errorf("index memory scenario: events per session %d must be positive", cfg.EventsPerSession)
	}
	if cfg.ValueSize <= 0 {
		return IndexMemoryResult{}, fmt.Errorf("index memory scenario: value size %d must be positive", cfg.ValueSize)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return IndexMemoryResult{}, fmt.Errorf("index memory scenario: create %s: %w", dir, err)
	}

	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return IndexMemoryResult{}, err
	}
	res, runErr := computeIndexMemorySeeded(store, cfg)
	closeErr := store.Close()
	if runErr != nil {
		return IndexMemoryResult{}, runErr
	}
	if closeErr != nil {
		return IndexMemoryResult{}, closeErr
	}
	return res, nil
}

// computeIndexMemorySeeded seeds the dataset on the open store and
// reads the figure; the caller owns the store's lifecycle.
func computeIndexMemorySeeded(store *elena.Store, cfg IndexMemoryConfig) (IndexMemoryResult, error) {
	if err := seedStore(store, cfg.Sessions, cfg.EventsPerSession, cfg.ValueSize); err != nil {
		return IndexMemoryResult{}, err
	}
	return ComputeIndexMemory(store.Stats())
}

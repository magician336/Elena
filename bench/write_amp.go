package bench

import (
	"fmt"
	"os"

	"elena"
)

// WriteAmplificationBasis is the accounting declaration that ships
// with every write-amplification number this scenario emits: flushed
// over logical bytes off the public Stats, with merge rewrites counted
// as flushed — the ratio therefore steps up with every merge, and only
// a long run shows the steady state.
const WriteAmplificationBasis = "flushed/logical bytes off Stats; merge rewrites count as flushed bytes, so the ratio steps up with every merge and only a long run shows the steady state"

// WriteAmplificationConfig declares the dataset the summary is measured
// over: EventsPerSession events of ValueSize bytes appended to each of
// Sessions trajectories, then one explicit merge. All fields must be
// positive.
type WriteAmplificationConfig struct {
	Sessions         int
	EventsPerSession int
	ValueSize        int
}

// WriteAmplificationSnapshot is one reading of the write-amplification
// pair: the cumulative logical payload the store accepted, the
// cumulative encoded bytes it put on disk, and the store's own ratio of
// the two.
type WriteAmplificationSnapshot struct {
	// LogicalBytes is Stats().BytesWritten.
	LogicalBytes uint64 `json:"logical_bytes"`

	// FlushedBytes is Stats().BytesFlushed.
	FlushedBytes uint64 `json:"flushed_bytes"`

	// Ratio is Stats().WriteAmplification.
	Ratio float64 `json:"ratio"`
}

// WriteAmplificationResult is the summary across one explicit merge:
// the byte pair before and after it, plus the flushed growth the merge
// itself caused — the rewrite made visible as a number, the declared
// accounting demonstrated rather than asserted.
type WriteAmplificationResult struct {
	BeforeMerge       WriteAmplificationSnapshot `json:"before_merge"`
	AfterMerge        WriteAmplificationSnapshot `json:"after_merge"`
	MergeFlushedBytes uint64                     `json:"merge_flushed_bytes"`
}

// WriteAmplificationSnapshotOf reads the byte pair off one Stats view.
func WriteAmplificationSnapshotOf(stats elena.Stats) WriteAmplificationSnapshot {
	return WriteAmplificationSnapshot{
		LogicalBytes:  stats.BytesWritten,
		FlushedBytes:  stats.BytesFlushed,
		Ratio:         stats.WriteAmplification,
	}
}

// RunWriteAmplification seeds the declared dataset into a fresh store
// under dir — which must not exist; the store creates it — reads the
// byte pair, runs one explicit public Merge, and reads the pair again.
// The merge rewrites the whole live dataset, so the delta between the
// two readings is the rewrite's own flushed cost: the accounting
// declaration above shown as numbers.
func RunWriteAmplification(dir string, cfg WriteAmplificationConfig) (WriteAmplificationResult, error) {
	if cfg.Sessions <= 0 {
		return WriteAmplificationResult{}, fmt.Errorf("write-amplification scenario: sessions %d must be positive", cfg.Sessions)
	}
	if cfg.EventsPerSession <= 0 {
		return WriteAmplificationResult{}, fmt.Errorf("write-amplification scenario: events per session %d must be positive", cfg.EventsPerSession)
	}
	if cfg.ValueSize <= 0 {
		return WriteAmplificationResult{}, fmt.Errorf("write-amplification scenario: value size %d must be positive", cfg.ValueSize)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return WriteAmplificationResult{}, fmt.Errorf("write-amplification scenario: create %s: %w", dir, err)
	}

	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return WriteAmplificationResult{}, err
	}
	res, runErr := runWriteAmplification(store, cfg)
	closeErr := store.Close()
	if runErr != nil {
		return WriteAmplificationResult{}, runErr
	}
	if closeErr != nil {
		return WriteAmplificationResult{}, closeErr
	}
	return res, nil
}

// runWriteAmplification performs the seed, the merge, and the two
// readings on the open store; the caller owns the store's lifecycle.
func runWriteAmplification(store *elena.Store, cfg WriteAmplificationConfig) (WriteAmplificationResult, error) {
	if err := seedStore(store, cfg.Sessions, cfg.EventsPerSession, cfg.ValueSize); err != nil {
		return WriteAmplificationResult{}, err
	}
	before := WriteAmplificationSnapshotOf(store.Stats())
	if err := store.Merge(); err != nil {
		return WriteAmplificationResult{}, fmt.Errorf("write-amplification scenario: merge: %w", err)
	}
	after := WriteAmplificationSnapshotOf(store.Stats())
	return WriteAmplificationResult{
		BeforeMerge:       before,
		AfterMerge:        after,
		MergeFlushedBytes: after.FlushedBytes - before.FlushedBytes,
	}, nil
}

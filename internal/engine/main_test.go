//go:build unix

package engine_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"elena/internal/engine"
)

// TestMain is the reexec entry of the fault-injection harness: when
// the writer-child environment variable is set, this process is the
// child writer and never runs any test — it writes, acks on stdout,
// and exits (or gets killed). See faultinject_test.go for why the
// harness kills a real process.
func TestMain(m *testing.M) {
	if os.Getenv(writerChildModeEnv) != "" {
		os.Exit(runWriterChild())
	}
	os.Exit(m.Run())
}

// runWriterChild is the writer-child main: open the storage directory
// from the environment, put every dataset entry through the public
// engine API, and print one ack line to stdout per Put that has
// already returned. The ack is strictly line-buffered and unordered
// with nothing: an ack means "the call returned", so under the default
// always policy the write is durable when the parent reads the line;
// under interval/none the durability semantics are the tier's own (the
// tests that use those tiers assert process-crash survival, not
// power-loss durability). All failures go to stderr with a non-zero
// exit so the parent reports the child stderr verbatim.
func runWriterChild() int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(os.Stderr, "writer child:", err)
		return 1
	}

	spec, err := writerSpecFromEnv()
	if err != nil {
		return fail(err)
	}

	opts := engine.Options{}
	switch spec.SyncPolicy {
	case "":
		// default: always
	case "always":
		opts.SyncPolicy = engine.SyncAlways
	case "interval":
		opts.SyncPolicy = engine.SyncInterval
		if spec.SyncPeriodMs > 0 {
			opts.SyncPeriod = time.Duration(spec.SyncPeriodMs) * time.Millisecond
		}
	case "none":
		opts.SyncPolicy = engine.SyncNone
	default:
		return fail(fmt.Errorf("unknown sync policy %q", spec.SyncPolicy))
	}
	if spec.RotateEvery > 0 {
		// Real engine rotation: size the threshold so the active file
		// turns roughly every RotateEvery puts (the dataset's entries
		// all share one size, see writerRotateThreshold).
		opts.MaxDataFileSize = writerRotateThreshold(spec)
	}
	eng, err := engine.Open(spec.Dir, opts)
	if err != nil {
		return fail(fmt.Errorf("open %q: %w", spec.Dir, err))
	}
	if spec.BatchSize > 0 {
		if err := runWriterChildBatches(eng, spec); err != nil {
			return fail(err)
		}
	} else {
		for _, w := range WriterDataset(spec) {
			if err := eng.Put([]byte(w.Key), w.Value); err != nil {
				return fail(fmt.Errorf("put %q: %w", w.Key, err))
			}
			// Ack only after Put has returned.
			if _, err := fmt.Fprintln(os.Stdout, w.Key); err != nil {
				return fail(fmt.Errorf("ack %q: %w", w.Key, err))
			}
		}
	}
	if err := eng.Close(); err != nil {
		return fail(fmt.Errorf("close: %w", err))
	}
	return 0
}

// runWriterChildBatches writes the dataset in batches of spec.BatchSize
// entries: one Batch Commit per group, then one ack line per entry —
// the acks print only after the whole batch is durable, so every ack
// the parent observes implies a whole committed batch. The window
// between batches (inside a Commit, before its acks print) is where a
// SIGKILL leaves a torn or absent batch that recovery must resolve to
// all-or-nothing.
func runWriterChildBatches(eng *engine.Engine, spec WriterSpec) error {
	dataset := WriterDataset(spec)
	for start := 0; start < len(dataset); start += spec.BatchSize {
		end := min(start+spec.BatchSize, len(dataset))
		b := eng.NewBatch()
		for _, w := range dataset[start:end] {
			if err := b.Put([]byte(w.Key), w.Value); err != nil {
				return fmt.Errorf("batch put %q: %w", w.Key, err)
			}
		}
		if err := b.Commit(); err != nil {
			return fmt.Errorf("batch commit %d..%d: %w", start, end, err)
		}
		for _, w := range dataset[start:end] {
			if _, err := fmt.Fprintln(os.Stdout, w.Key); err != nil {
				return fmt.Errorf("ack %q: %w", w.Key, err)
			}
		}
	}
	return nil
}

// writerSpecFromEnv decodes the WriterSpec the parent encoded into the
// child's environment.
func writerSpecFromEnv() (WriterSpec, error) {
	spec := WriterSpec{Dir: os.Getenv(writerDirEnv)}
	var err error
	if spec.Writes, err = strconv.Atoi(os.Getenv(writerWritesEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerWritesEnv, err)
	}
	seed, err := strconv.ParseInt(os.Getenv(writerSeedEnv), 10, 64)
	if err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerSeedEnv, err)
	}
	spec.Seed = seed
	if spec.ValueSize, err = strconv.Atoi(os.Getenv(writerValueSizeEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerValueSizeEnv, err)
	}
	if spec.RotateEvery, err = strconv.Atoi(os.Getenv(writerRotateEveryEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerRotateEveryEnv, err)
	}
	if spec.BatchSize, err = strconv.Atoi(os.Getenv(writerBatchSizeEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerBatchSizeEnv, err)
	}
	spec.SyncPolicy = os.Getenv(writerSyncPolicyEnv)
	if spec.SyncPeriodMs, err = strconv.Atoi(os.Getenv(writerSyncPeriodMsEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerSyncPeriodMsEnv, err)
	}
	if spec.Dir == "" || spec.Writes <= 0 || spec.ValueSize <= 0 {
		return spec, fmt.Errorf("incomplete writer spec: %+v", spec)
	}
	return spec, nil
}

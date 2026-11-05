//go:build unix

package elena_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/magician336/elena"
)

// TestMain is the reexec entry of the semantic layer's
// fault-injection harness: when the writer-child environment variable
// is set, this process is the child writer and never runs any test —
// it opens the store, appends and checkpoints through the public elena
// API, acks on stdout, and exits (or gets killed). See
// faultinject_test.go for why the harness kills a real process.
func TestMain(m *testing.M) {
	if os.Getenv(semanticWriterModeEnv) != "" {
		os.Exit(runSemanticWriterChild())
	}
	os.Exit(m.Run())
}

// runSemanticWriterChild is the writer-child main: open the store from
// the environment, create the sessions, and run the schedule the
// parent predicts — one ack line per Append that has returned, one per
// plain checkpoint, and in batch mode the group's ack lines only after
// the Commit that made the events and the checkpoint durable together
// returned. The ack is strictly line-buffered and unordered with
// nothing: an ack means "the call returned", and every write rides the
// engine's default always-sync regime, so an acknowledged event or
// checkpoint is durable the moment the parent reads the line. All
// failures go to stderr with a non-zero exit so the parent reports the
// child stderr verbatim.
func runSemanticWriterChild() int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(os.Stderr, "semantic writer child:", err)
		return 1
	}

	spec, err := semanticWriterSpecFromEnv()
	if err != nil {
		return fail(err)
	}
	payloads := semanticWriterPayloads(spec)

	st, err := elena.Open(spec.Dir)
	if err != nil {
		return fail(fmt.Errorf("open %q: %w", spec.Dir, err))
	}
	sessions := make([]*elena.Session, spec.Sessions)
	for i := range sessions {
		sess, err := st.CreateSession(semanticWriterNamespace, fmt.Sprintf("s%d", i))
		if err != nil {
			return fail(fmt.Errorf("create session s%d: %w", i, err))
		}
		sessions[i] = sess
	}
	ackEvent := func(e int, seq uint64) error {
		if _, err := fmt.Fprintf(os.Stdout, "A %d %d\n", e%spec.Sessions, seq); err != nil {
			return fmt.Errorf("ack event %d: %w", e, err)
		}
		return nil
	}
	ackCheckpoint := func(id uint64) error {
		if _, err := fmt.Fprintf(os.Stdout, "C %d\n", id); err != nil {
			return fmt.Errorf("ack checkpoint %d: %w", id, err)
		}
		return nil
	}

	if spec.BatchSize > 0 {
		for start := 0; start < spec.Events; start += spec.BatchSize {
			end := min(start+spec.BatchSize, spec.Events)
			batch := st.NewBatch()
			for e := start; e < end; e++ {
				if err := batch.Append(sessions[e%spec.Sessions], payloads[e]); err != nil {
					return fail(fmt.Errorf("batch append %d: %w", e, err))
				}
			}
			batch.Checkpoint()
			seqs, id, err := batch.Commit()
			if err != nil {
				return fail(fmt.Errorf("batch commit %d..%d: %w", start, end, err))
			}
			if len(seqs) != end-start {
				return fail(fmt.Errorf("batch commit %d..%d returned %d sequence numbers, want %d", start, end, len(seqs), end-start))
			}
			for i, seq := range seqs {
				if err := ackEvent(start+i, seq); err != nil {
					return fail(err)
				}
			}
			if err := ackCheckpoint(id); err != nil {
				return fail(err)
			}
		}
	} else {
		counts := make([]uint64, spec.Sessions)
		for e := 0; e < spec.Events; e++ {
			idx := e % spec.Sessions
			seq, err := sessions[idx].Append(payloads[e])
			if err != nil {
				return fail(fmt.Errorf("append %d to s%d: %w", e, idx, err))
			}
			if seq != counts[idx]+1 {
				return fail(fmt.Errorf("append %d to s%d returned seq %d, want %d", e, idx, seq, counts[idx]+1))
			}
			counts[idx] = seq
			if err := ackEvent(e, seq); err != nil {
				return fail(err)
			}
			if (e+1)%spec.CheckpointEvery == 0 {
				id, err := st.CreateCheckpoint()
				if err != nil {
					return fail(fmt.Errorf("create checkpoint after event %d: %w", e, err))
				}
				if err := ackCheckpoint(id); err != nil {
					return fail(err)
				}
			}
		}
	}
	if err := st.Close(); err != nil {
		return fail(fmt.Errorf("close: %w", err))
	}
	return 0
}

// semanticWriterSpecFromEnv decodes the SemanticWriterSpec the parent
// encoded into the child's environment.
func semanticWriterSpecFromEnv() (SemanticWriterSpec, error) {
	spec := SemanticWriterSpec{Dir: os.Getenv(semanticWriterDirEnv)}
	var err error
	if spec.Sessions, err = strconv.Atoi(os.Getenv(semanticWriterSessionsEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterSessionsEnv, err)
	}
	if spec.Events, err = strconv.Atoi(os.Getenv(semanticWriterEventsEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterEventsEnv, err)
	}
	seed, err := strconv.ParseInt(os.Getenv(semanticWriterSeedEnv), 10, 64)
	if err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterSeedEnv, err)
	}
	spec.Seed = seed
	if spec.PayloadSize, err = strconv.Atoi(os.Getenv(semanticWriterPayloadEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterPayloadEnv, err)
	}
	if spec.CheckpointEvery, err = strconv.Atoi(os.Getenv(semanticWriterCheckpointEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterCheckpointEnv, err)
	}
	if spec.BatchSize, err = strconv.Atoi(os.Getenv(semanticWriterBatchEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", semanticWriterBatchEnv, err)
	}
	if err := validateSemanticWriterSpec(spec); err != nil {
		return spec, fmt.Errorf("writer spec from env: %w", err)
	}
	return spec, nil
}

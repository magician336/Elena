//go:build unix

package main

import (
	"fmt"
	"os"
	"testing"

	"elena"
)

// TestMain is the reexec entry of the CLI's full-cycle harness, in the
// 06 form: when the writer-child environment variable is set, this
// process is the child writer and never runs any test — it opens the
// store through the public elena library, drives the fixed
// write-checkpoint-write schedule, acks each returned call on stdout,
// and gets killed mid-schedule. See fullcycle_test.go for the flow.
func TestMain(m *testing.M) {
	if os.Getenv(fullCycleModeEnv) != "" {
		os.Exit(runFullCycleWriterChild())
	}
	os.Exit(m.Run())
}

// runFullCycleWriterChild is the writer-child main: open the store
// from the environment, attach to both sessions, and run the shared
// schedule — the pre-checkpoint round, one checkpoint, the
// post-checkpoint round. One ack line per Append and per checkpoint
// that has returned, strictly line-buffered and unordered with
// nothing: an ack means "the call returned", and every write rides the
// engine's default always-sync regime, so an acknowledged event or
// checkpoint is durable the moment the parent reads the line. All
// failures go to stderr with a non-zero exit so the parent reports
// them verbatim. The closing Close is the safety net the parent's
// SIGKILL must preempt — reaching it cleanly fails the harness instead
// of silently passing a run no fault touched.
func runFullCycleWriterChild() int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(os.Stderr, "full-cycle writer child:", err)
		return 1
	}

	dir := os.Getenv(fullCycleDirEnv)
	if dir == "" {
		return fail(fmt.Errorf("%s unset", fullCycleDirEnv))
	}
	store, err := elena.Open(dir)
	if err != nil {
		return fail(fmt.Errorf("open %q: %w", dir, err))
	}
	sessions := make([]*elena.Session, fullCycleSessions)
	for s := range sessions {
		sessions[s], err = store.CreateSession(fullCycleNamespace, fullCycleSessionName(s))
		if err != nil {
			return fail(fmt.Errorf("create session %s: %w", fullCycleSessionName(s), err))
		}
	}

	pre, post := fullCycleRounds()
	for n := pre[0]; n <= pre[1]; n++ {
		for s := range sessions {
			if err := appendAndAck(sessions[s], s, n); err != nil {
				return fail(err)
			}
		}
	}
	id, err := store.CreateCheckpoint()
	if err != nil {
		return fail(fmt.Errorf("create checkpoint: %w", err))
	}
	if _, err := fmt.Fprintf(os.Stdout, "C %d\n", id); err != nil {
		return fail(fmt.Errorf("ack checkpoint %d: %w", id, err))
	}
	for n := post[0]; n <= post[1]; n++ {
		for s := range sessions {
			if err := appendAndAck(sessions[s], s, n); err != nil {
				return fail(err)
			}
		}
	}
	if err := store.Close(); err != nil {
		return fail(fmt.Errorf("close: %w", err))
	}
	return 0
}

// appendAndAck appends session s's n-th payload and acks it —
// "A <s> <n>" — after the call returned. The sequence number must
// equal n: the schedule is a serial writer, so any divergence means
// the child and the parent's prediction no longer describe the same
// run.
func appendAndAck(sess *elena.Session, s, n int) error {
	payload := fullCyclePayload(s, n)
	seq, err := sess.Append([]byte(payload))
	if err != nil {
		return fmt.Errorf("append %s: %w", payload, err)
	}
	if seq != uint64(n) {
		return fmt.Errorf("append %s returned seq %d, want %d", payload, seq, n)
	}
	if _, err := fmt.Fprintf(os.Stdout, "A %d %d\n", s, n); err != nil {
		return fmt.Errorf("ack %s: %w", payload, err)
	}
	return nil
}

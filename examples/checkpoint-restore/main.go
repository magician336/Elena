// Checkpoint restore — an event group and its baseline checkpoint
// committed in one atomic batch, more events appended after the
// baseline, a close that stands in for a crash, and a reopen that
// restores from the stored checkpoint. The closing contrast is the
// restore boundary itself: the point-in-time read stops at the
// baseline, while the full read still shows the post-baseline events —
// a baseline to recover to, not a time machine. Checkpoints like these
// suit agent sessions that must resume from a known consistent point.
//
// Checkpoint demo, run with:
//
//	go run ./examples/checkpoint-restore [-dir DIR]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"elena"
)

// Store directory and stdout wiring for run: -dir overrides the default
// fresh temporary directory, and a failure exits non-zero.
func main() {
	dir := flag.String("dir", "", "store directory; empty means a fresh temporary directory")
	flag.Parse()

	storeDir := *dir
	if storeDir == "" {
		temp, err := os.MkdirTemp("", "elena-checkpoint-restore-")
		if err != nil {
			reportFailure(err)
			os.Exit(1)
		}
		storeDir = temp
	}
	if err := run(os.Stdout, storeDir); err != nil {
		reportFailure(err)
		os.Exit(1)
	}
}

// reportFailure prints the demo's terminal error; stderr write failures
// have nothing left to report to.
func reportFailure(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "checkpoint restore demo failed:", err)
}

// narrater carries the demo's narration writer and its first write
// failure: every line goes through say, and the sticky error surfaces
// from run — a broken narration pipe ends the demo instead of failing
// silently.
type narrater struct {
	w   io.Writer
	err error
}

// say writes one formatted narration line; write errors are sticky.
func (n *narrater) say(format string, args ...any) {
	if n.err != nil {
		return
	}
	_, n.err = fmt.Fprintf(n.w, format, args...)
}

// run narrates the demo against the store rooted at dir. Sequence
// numbers, payloads, and checkpoint IDs are deterministic; timestamps
// follow the clock.
func run(w io.Writer, dir string) (err error) {
	n := &narrater{w: w}
	n.say("store directory: %s\n", dir)

	st, err := elena.Open(dir)
	if err != nil {
		return fmt.Errorf("store open %q: %w", dir, err)
	}
	defer func() { _ = st.Close() }() // best effort on error paths; Close is idempotent

	sess, err := st.CreateSession("runs", "alpha")
	if err != nil {
		return fmt.Errorf("session create: %w", err)
	}
	n.say("session created: namespace %q, session %q, lifecycle perpetual\n", "runs", "alpha")

	for _, payload := range []string{"task opened", "tool result: schema loaded"} {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return fmt.Errorf("event append %q: %w", payload, err)
		}
		n.say("event appended: seq %d, payload %q\n", seq, payload)
	}

	// Event group plus baseline: one batch, one atomic commit.
	batch := st.NewBatch()
	for _, payload := range []string{
		"tool result: page fetched",
		"tool result: rows decoded",
		"plan revised: 3 steps",
	} {
		if err := batch.Append(sess, []byte(payload)); err != nil {
			return fmt.Errorf("batch append %q: %w", payload, err)
		}
	}
	batch.Checkpoint()
	seqs, cpID, err := batch.Commit()
	if err != nil {
		return fmt.Errorf("batch commit: %w", err)
	}
	n.say("batch committed: seqs %v, checkpoint id %d — one append, one fdatasync: the event group and its baseline survive as a whole\n", seqs, cpID)

	// Post-baseline events: written after the checkpoint, still ordinary
	// appends.
	for _, payload := range []string{"interim answer stored", "final answer stored"} {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return fmt.Errorf("event append %q: %w", payload, err)
		}
		n.say("event appended: seq %d, payload %q\n", seq, payload)
	}

	if err := st.Close(); err != nil {
		return fmt.Errorf("store close: %w", err)
	}
	n.say("store closed: acknowledged events and the checkpoint are durable — the demo's stand-in for a crash\n")

	// Reopen: the stored checkpoint is ordinary data on disk.
	st, err = elena.Open(dir)
	if err != nil {
		return fmt.Errorf("store reopen %q: %w", dir, err)
	}
	n.say("store reopened: same directory, same lock discipline\n")

	checkpoints, err := st.Checkpoints()
	if err != nil {
		return fmt.Errorf("checkpoints listing: %w", err)
	}
	for _, cp := range checkpoints {
		n.say("checkpoints listed: id %d (created-at %s)\n", cp.ID, cp.CreatedAt.UTC().Format(time.RFC3339Nano))
	}

	baselineSeq := seqs[len(seqs)-1]
	if err := st.Restore(cpID); err != nil {
		return fmt.Errorf("restore of checkpoint %d: %w", cpID, err)
	}
	n.say("restore applied: checkpoint id %d — sequence counters back at the baseline's seq %d; a baseline to recover to, not a time machine\n", cpID, baselineSeq)

	// Resume: the handle continues the stored trajectory, and the
	// restored counter hands out the baseline's next sequence number.
	sess, err = st.CreateSession("runs", "alpha")
	if err != nil {
		return fmt.Errorf("session resume: %w", err)
	}
	seq, err := sess.Append([]byte("resumed after restore"))
	if err != nil {
		return fmt.Errorf("event append %q: %w", "resumed after restore", err)
	}
	n.say("event appended: seq %d, payload %q — the baseline's next sequence number, overwriting the pre-restore seq %d; last write wins\n",
		seq, "resumed after restore", seq)

	// Contrast: the point-in-time read stops at the baseline; the full
	// read still shows everything on disk, post-baseline events
	// included.
	past, err := sess.EventsUpTo(baselineSeq)
	if err != nil {
		return fmt.Errorf("point-in-time read to seq %d: %w", baselineSeq, err)
	}
	n.say("point-in-time read to seq %d: %d events — the trajectory as of the baseline\n", baselineSeq, len(past))
	for _, ev := range past {
		n.say("  seq %d payload %q\n", ev.Seq, ev.Payload)
	}

	events, err := sess.Events()
	if err != nil {
		return fmt.Errorf("trajectory full read: %w", err)
	}
	n.say("trajectory full read: %d events — data after the baseline is still on disk and still visible\n", len(events))
	for _, ev := range events {
		n.say("  seq %d payload %q\n", ev.Seq, ev.Payload)
	}

	if err := st.Close(); err != nil {
		return fmt.Errorf("store close: %w", err)
	}
	n.say("store closed: acknowledged events are durable\n")
	return n.err
}

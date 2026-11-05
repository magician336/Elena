// TTL lifecycle, both halves — a timed session whose whole content —
// metadata and events alike — reads as if never written once the
// deadline passes (the visibility half, immediate and free on the read
// path), and the background sweep whose triggered merge returns the
// session's bytes and index memory to the disk (the space half, Stats
// before and after). Timed sessions like these suit agent scratch
// state: intermediate results worth keeping briefly and dropping
// whole.
//
// TTL demo, run with:
//
//	go run ./examples/ttl-lifecycle [-dir DIR] [-ttl 1s] [-sweep 4s]
//
// Timing contract of the narration: the sweep period must exceed the
// ttl by a comfortable margin, so the "expired but not yet reclaimed"
// observation lands between the deadline and the first sweep check
// that can merge.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/magician336/elena"
)

// Store directory and pacing wiring for run: -dir overrides the default
// fresh temporary directory, -ttl sets the session's lifetime, -sweep
// sets the background sweep's period, and a failure exits non-zero.
func main() {
	dir := flag.String("dir", "", "store directory; empty means a fresh temporary directory")
	ttl := flag.Duration("ttl", time.Second, "session lifetime for the demo")
	sweep := flag.Duration("sweep", 4*time.Second, "background sweep period for the demo")
	flag.Parse()

	storeDir := *dir
	if storeDir == "" {
		temp, err := os.MkdirTemp("", "elena-ttl-lifecycle-")
		if err != nil {
			reportFailure(err)
			os.Exit(1)
		}
		storeDir = temp
	}
	if err := run(os.Stdout, storeDir, *ttl, *sweep); err != nil {
		reportFailure(err)
		os.Exit(1)
	}
}

// reportFailure prints the demo's terminal error; stderr write failures
// have nothing left to report to.
func reportFailure(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "TTL lifecycle demo failed:", err)
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

// Deadline-bounded poll: cond is checked every 10ms until it holds or
// the bound elapses; a wedged store fails the demo instead of hanging
// it.
func waitUntil(what string, bound time.Duration, cond func() bool) error {
	deadline := time.Now().Add(bound)
	for !cond() {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: timed out after %s", what, bound)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// run narrates the demo against the store rooted at dir: the timed
// session's visible life, its invisibility after the deadline — with
// the bytes still standing — and the sweep-triggered merge that returns
// them. Sequence numbers and Stats figures are deterministic; the
// deadline and timestamps follow the clock.
func run(w io.Writer, dir string, ttl, sweepPeriod time.Duration) (err error) {
	n := &narrater{w: w}
	n.say("store directory: %s\n", dir)

	st, err := elena.Open(dir, elena.WithSweepPeriod(sweepPeriod))
	if err != nil {
		return fmt.Errorf("store open %q: %w", dir, err)
	}
	defer func() { _ = st.Close() }() // best effort on error paths; Close is idempotent
	n.say("store opened: sweep period %s (default: one check per minute; see WithSweepPeriod)\n", sweepPeriod)

	sess, err := st.CreateSessionWithTTL("runs", "scratch", ttl)
	if err != nil {
		return fmt.Errorf("session create: %w", err)
	}
	n.say("session created: namespace %q, session %q, ttl %s — one deadline shared by the session's metadata and every event\n",
		"runs", "scratch", ttl)

	for _, payload := range []string{
		"scratch value: 42",
		"scratch page fetched",
		"scratch fact extracted",
	} {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return fmt.Errorf("event append %q: %w", payload, err)
		}
		n.say("event appended: seq %d, payload %q\n", seq, payload)
	}

	// Visibility half, before the deadline: the session reads normally
	// and occupies the disk.
	live, err := sess.Events()
	if err != nil {
		return fmt.Errorf("events read before the deadline: %w", err)
	}
	n.say("events read before the deadline: %d events\n", len(live))
	sessions, err := st.Sessions("runs")
	if err != nil {
		return fmt.Errorf("sessions listing before the deadline: %w", err)
	}
	n.say("sessions listed before the deadline: %d session", len(sessions))
	for _, info := range sessions {
		n.say(", %s deadline %s", info.ID, info.Deadline.UTC().Format(time.RFC3339Nano))
	}
	n.say("\n")
	before := st.Stats()
	n.say("stats before the deadline: live entries %d, live bytes %d, data bytes %d, dead ratio %.2f\n",
		before.LiveEntries, before.LiveBytes, before.DataBytes, before.DeadRatio)

	// Visibility half, after the deadline: the whole session reads as
	// if never written. Expiry is lazy — the poll observes the read
	// path itself, not a signal.
	if err := waitUntil("session invisibility", ttl+2*sweepPeriod, func() bool {
		events, err := sess.Events()
		return err == nil && len(events) == 0
	}); err != nil {
		return fmt.Errorf("visibility half: %w", err)
	}
	n.say("deadline reached (ttl %s): the whole session — metadata and events — reads as if never written\n", ttl)

	events, err := sess.Events()
	if err != nil {
		return fmt.Errorf("events read after the deadline: %w", err)
	}
	n.say("events read after the deadline: %d events\n", len(events))
	if _, err := sess.Event(1); !errors.Is(err, elena.ErrEventNotFound) {
		return fmt.Errorf("event read after the deadline: want ErrEventNotFound, got %v", err)
	}
	n.say("event read after the deadline: seq 1: ErrEventNotFound\n")
	if _, err := sess.Append([]byte("late write")); !errors.Is(err, elena.ErrSessionEnded) {
		return fmt.Errorf("append after the deadline: want ErrSessionEnded, got %v", err)
	}
	n.say("append after the deadline: ErrSessionEnded — the session already reads as if never written; the name is free for a fresh session\n")
	sessions, err = st.Sessions("runs")
	if err != nil {
		return fmt.Errorf("sessions listing after the deadline: %w", err)
	}
	listed := "none"
	if len(sessions) > 0 {
		listed = fmt.Sprintf("%d sessions", len(sessions))
	}
	n.say("sessions listed after the deadline: %s\n", listed)

	// Occupancy after expiry: invisible is not reclaimed. The bytes
	// stand until a merge omits them. The pacing contract (sweep
	// period comfortably above the ttl) makes this observation safe;
	// a violation fails the demo instead of printing a false
	// "unchanged".
	between := st.Stats()
	if between.DataBytes != before.DataBytes {
		return fmt.Errorf("merge half: data bytes moved from %d to %d before the post-deadline observation — sweep period %s is too close to ttl %s",
			before.DataBytes, between.DataBytes, sweepPeriod, ttl)
	}
	n.say("stats after the deadline: data bytes %d, unchanged from %d — visibility and occupancy are separate facts; reclaiming the bytes falls to the sweep\n",
		between.DataBytes, before.DataBytes)

	// Space half: the sweep's next check sees the elapsed deadline and
	// triggers one merge; the merge rewrites the live entries and omits
	// the expired ones — omission is the reclamation.
	if err := waitUntil("merge reclamation", 3*sweepPeriod, func() bool {
		return st.Stats().DataBytes == 0
	}); err != nil {
		return fmt.Errorf("merge half: %w", err)
	}
	n.say("merge ran: the sweep noticed the elapsed deadline and triggered one merge — expired entries omitted, bytes and index memory returned\n")
	after := st.Stats()
	n.say("stats after the merge: live entries %d, live bytes %d, data bytes %d, dead ratio %.2f\n",
		after.LiveEntries, after.LiveBytes, after.DataBytes, after.DeadRatio)

	events, err = sess.Events()
	if err != nil {
		return fmt.Errorf("events read after the merge: %w", err)
	}
	n.say("events read after the merge: %d events — visibility never moved\n", len(events))

	if err := st.Close(); err != nil {
		return fmt.Errorf("store close: %w", err)
	}
	n.say("store closed: acknowledged events are durable\n")
	return n.err
}

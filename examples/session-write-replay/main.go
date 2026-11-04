// Session writes and replay — one perpetual session in one namespace,
// four appended events, the full trajectory read, and a point-in-time
// read back to a known consistent sequence number. Sessions of this
// shape suit agent conversation records, one appended event per
// exchange.
//
// Session demo, run with:
//
//	go run ./examples/session-write-replay [-dir DIR]
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
		temp, err := os.MkdirTemp("", "elena-session-replay-")
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
	_, _ = fmt.Fprintln(os.Stderr, "session replay demo failed:", err)
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

// run narrates the demo against the store rooted at dir: session
// creation, appends with their handed-out sequence numbers, the full
// trajectory read — sequence numbers and append timestamps travel with
// every event — and a point-in-time read to a known consistent sequence
// number. Sequence numbers and payloads are deterministic; append
// timestamps follow the clock.
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

	for _, payload := range []string{
		"session opened",
		"tool result: 3 matches",
		"plan revised: 2 steps",
		"answer drafted",
	} {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return fmt.Errorf("event append %q: %w", payload, err)
		}
		n.say("event appended: seq %d, payload %q\n", seq, payload)
	}

	events, err := sess.Events()
	if err != nil {
		return fmt.Errorf("trajectory full read: %w", err)
	}
	n.say("trajectory full read: %d events\n", len(events))
	for _, ev := range events {
		n.say("  seq %d appended-at %s payload %q\n",
			ev.Seq, ev.AppendedAt.UTC().Format(time.RFC3339Nano), ev.Payload)
	}

	const knownSeq = 2
	past, err := sess.EventsUpTo(knownSeq)
	if err != nil {
		return fmt.Errorf("point-in-time read to seq %d: %w", knownSeq, err)
	}
	n.say("point-in-time read to seq %d: %d events — the trajectory as of a known consistent point\n",
		knownSeq, len(past))
	for _, ev := range past {
		n.say("  seq %d payload %q\n", ev.Seq, ev.Payload)
	}

	if err := st.Close(); err != nil {
		return fmt.Errorf("store close: %w", err)
	}
	n.say("store closed: acknowledged events are durable\n")
	return n.err
}

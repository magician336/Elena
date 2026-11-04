package elena_test

import (
	"testing"
	"time"
)

// A point-in-time read must return the trajectory's events up to and
// including the requested sequence number, in sequence order, with the
// endpoint event present and self-describing.
func TestEventsUpToReturnsEventsThroughTheBoundInclusive(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	payloads := []string{"first", "second", "third", "fourth"}
	start := time.Now().Add(-time.Minute)
	for _, p := range payloads {
		mustAppend(t, sess, p)
	}
	end := time.Now().Add(time.Minute)

	events, err := sess.EventsUpTo(2)
	if err != nil {
		t.Fatalf("EventsUpTo(2) failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("EventsUpTo(2) returned %d events, want 2", len(events))
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Errorf("event %d has Seq = %d, want %d", i, ev.Seq, i+1)
		}
		if string(ev.Payload) != payloads[i] {
			t.Errorf("event %d payload = %q, want %q", i, ev.Payload, payloads[i])
		}
		if ev.AppendedAt.Before(start) || ev.AppendedAt.After(end) {
			t.Errorf("event %d AppendedAt = %v, want within the test window", i, ev.AppendedAt)
		}
	}
}

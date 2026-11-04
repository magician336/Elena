package elena_test

import (
	"testing"
	"time"

	"elena"
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

// The bound is an absolute ceiling on sequence numbers, so the edges
// read as prefixes: a seq below the trajectory's first sequence number
// reads empty, a seq at the stored tail includes that tail event, a seq
// beyond the tail reads the whole trajectory, and an empty trajectory
// reads empty at any seq.
func TestEventsUpToBoundEdges(t *testing.T) {
	st := openStore(t)
	empty := createSession(t, st, "runs", "empty")
	filled := createSession(t, st, "runs", "alpha")
	for _, p := range []string{"first", "second", "third"} {
		mustAppend(t, filled, p)
	}

	cases := []struct {
		name  string
		sess  *elena.Session
		seq   uint64
		want  int
		first bool // whether event 1 must be present, anchoring order
	}{
		{"below the first sequence number", filled, 0, 0, false},
		{"between stored events", filled, 2, 2, true},
		{"exactly at the stored tail", filled, 3, 3, true},
		{"beyond the stored tail", filled, 99, 3, true},
		{"empty trajectory at zero", empty, 0, 0, false},
		{"empty trajectory beyond zero", empty, 5, 0, false},
	}
	for _, tc := range cases {
		events, err := tc.sess.EventsUpTo(tc.seq)
		if err != nil {
			t.Fatalf("EventsUpTo(%d) %s failed: %v", tc.seq, tc.name, err)
		}
		if len(events) != tc.want {
			t.Errorf("EventsUpTo(%d) %s returned %d events, want %d", tc.seq, tc.name, len(events), tc.want)
			continue
		}
		if tc.first {
			if events[0].Seq != 1 {
				t.Errorf("EventsUpTo(%d) %s starts at Seq = %d, want 1", tc.seq, tc.name, events[0].Seq)
			}
			if last := events[len(events)-1].Seq; last != uint64(len(events)) {
				t.Errorf("EventsUpTo(%d) %s ends at Seq = %d, want %d", tc.seq, tc.name, last, len(events))
			}
		}
	}
}

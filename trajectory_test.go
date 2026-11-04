package elena_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"elena"
)

// mustAppend appends payload and fails the test on error.
func mustAppend(t *testing.T, sess *elena.Session, payload string) {
	t.Helper()
	if _, err := sess.Append([]byte(payload)); err != nil {
		t.Fatalf("Append(%q) failed: %v", payload, err)
	}
}

// A full read must return the whole trajectory in sequence order with
// payload bytes intact and self-describing events: sequence number and
// append time are present without any external bookkeeping.
func TestEventsReturnsWholeTrajectoryInSequenceOrder(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	payloads := []string{"first", "second", "third"}
	start := time.Now().Add(-time.Minute)
	for _, p := range payloads {
		mustAppend(t, sess, p)
	}
	end := time.Now().Add(time.Minute)

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() failed: %v", err)
	}
	if len(events) != len(payloads) {
		t.Fatalf("Events() returned %d events, want %d", len(events), len(payloads))
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

// A trajectory with no events must read back as empty, not as an error.
func TestEventsOnEmptyTrajectoryReturnsNoEvents(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() on empty trajectory failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() on empty trajectory returned %d events, want 0", len(events))
	}
}

// A single event must be retrievable by its sequence number alone,
// without scanning the trajectory.
func TestEventReturnsSingleEventBySequence(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")
	for _, p := range []string{"first", "second", "third"} {
		mustAppend(t, sess, p)
	}

	ev, err := sess.Event(2)
	if err != nil {
		t.Fatalf("Event(2) failed: %v", err)
	}
	if ev.Seq != 2 {
		t.Errorf("Event(2).Seq = %d, want 2", ev.Seq)
	}
	if string(ev.Payload) != "second" {
		t.Errorf("Event(2) payload = %q, want %q", ev.Payload, "second")
	}
	if ev.AppendedAt.IsZero() {
		t.Error("Event(2).AppendedAt is zero, want the stored append time")
	}
}

// Requesting a sequence number the trajectory does not hold — zero,
// beyond the tail, or on an empty trajectory — must report
// ErrEventNotFound.
func TestEventUnknownSequenceReturnsErrEventNotFound(t *testing.T) {
	st := openStore(t)
	empty := createSession(t, st, "runs", "empty")
	filled := createSession(t, st, "runs", "alpha")
	mustAppend(t, filled, "first")

	for _, tc := range []struct {
		sess *elena.Session
		seq  uint64
	}{
		{empty, 1},
		{filled, 0},
		{filled, 2},
	} {
		if _, err := tc.sess.Event(tc.seq); !errors.Is(err, elena.ErrEventNotFound) {
			t.Errorf("Event(%d) err = %v, want ErrEventNotFound", tc.seq, err)
		}
	}
}

// Trajectories must stay isolated even for labels chosen so one raw
// namespace or session string is a prefix of another's: the
// length-prefixed key encoding guarantees no nesting, and each
// trajectory numbers its events independently from 1.
func TestTrajectoriesStayIsolatedAcrossLabels(t *testing.T) {
	st := openStore(t)

	ids := []struct{ namespace, session string }{
		{"a", "b"},
		{"a", "bc"},
		{"ab", "b"},
		{"ab", "bc"},
	}
	handles := make([]*elena.Session, len(ids))
	payloads := make([]string, len(ids))
	for i, id := range ids {
		handles[i] = createSession(t, st, id.namespace, id.session)
		payloads[i] = fmt.Sprintf("payload-%s-%s", id.namespace, id.session)
	}

	const rounds = 2
	for round := 1; round <= rounds; round++ {
		for i, sess := range handles {
			seq, err := sess.Append([]byte(payloads[i]))
			if err != nil {
				t.Fatalf("Append() for %s/%s failed: %v", ids[i].namespace, ids[i].session, err)
			}
			if want := uint64(round); seq != want {
				t.Fatalf("Append() for %s/%s seq = %d, want %d", ids[i].namespace, ids[i].session, seq, want)
			}
		}
	}

	for i, sess := range handles {
		events, err := sess.Events()
		if err != nil {
			t.Fatalf("Events() for %s/%s failed: %v", ids[i].namespace, ids[i].session, err)
		}
		if len(events) != rounds {
			t.Fatalf("Events() for %s/%s returned %d events, want %d", ids[i].namespace, ids[i].session, len(events), rounds)
		}
		for j, ev := range events {
			if ev.Seq != uint64(j+1) {
				t.Errorf("trajectory %s/%s event %d Seq = %d, want %d", ids[i].namespace, ids[i].session, j, ev.Seq, j+1)
			}
			if string(ev.Payload) != payloads[i] {
				t.Errorf("trajectory %s/%s event %d payload = %q, want %q", ids[i].namespace, ids[i].session, j, ev.Payload, payloads[i])
			}
		}
	}
}

// After Close, every read path must fail with an error wrapping
// ErrClosed, like the append path.
func TestReadsAfterCloseReturnErrClosed(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "first")
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	if _, err := sess.Events(); !errors.Is(err, elena.ErrClosed) {
		t.Errorf("Events() after Close err = %v, want ErrClosed", err)
	}
	if _, err := sess.EventsUpTo(1); !errors.Is(err, elena.ErrClosed) {
		t.Errorf("EventsUpTo(1) after Close err = %v, want ErrClosed", err)
	}
	if _, err := sess.Event(1); !errors.Is(err, elena.ErrClosed) {
		t.Errorf("Event(1) after Close err = %v, want ErrClosed", err)
	}
}

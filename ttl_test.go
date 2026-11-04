package elena_test

import (
	"errors"
	"testing"
	"time"

	"elena"
)

// createTimedSession creates the session named by namespace and session
// with a TTL and fails the test on error.
func createTimedSession(t *testing.T, st *elena.Store, namespace, session string, ttl time.Duration) *elena.Session {
	t.Helper()
	sess, err := st.CreateSessionWithTTL(namespace, session, ttl)
	if err != nil {
		t.Fatalf("CreateSessionWithTTL(%q, %q, %s) failed: %v", namespace, session, ttl, err)
	}
	return sess
}

// waitForTTLElapse blocks until well after the given TTL has elapsed, so
// every entry of a session created with it is past its deadline.
func waitForTTLElapse(ttl time.Duration) {
	time.Sleep(ttl + 600*time.Millisecond)
}

// A session created with a TTL shares one deadline between its metadata
// and every event of its trajectory: before the deadline the trajectory
// reads in full; after it, the whole session — events included — reads
// as if never written, both in the full read and in every single-event
// read.
func TestTimedSessionVanishesWholeWhenTTLElapses(t *testing.T) {
	st := openStore(t)
	const ttl = 500 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "alpha", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() before the deadline failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Events() before the deadline returned %d events, want 3", len(events))
	}

	waitForTTLElapse(ttl)

	events, err = sess.Events()
	if err != nil {
		t.Fatalf("Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the deadline returned %d events, want 0", len(events))
	}
	for seq := uint64(1); seq <= 3; seq++ {
		if _, err := sess.Event(seq); !errors.Is(err, elena.ErrEventNotFound) {
			t.Errorf("Event(%d) after the deadline err = %v, want ErrEventNotFound", seq, err)
		}
	}
}

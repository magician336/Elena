package elena_test

import (
	"errors"
	"math"
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

// CreateSessionWithTTL must reject TTLs that cannot define a future
// deadline — zero, negative, or beyond the storable range — with
// ErrInvalidTTL, and leave no session behind.
func TestCreateSessionWithTTLRejectsInvalidTTL(t *testing.T) {
	st := openStore(t)

	for _, ttl := range []time.Duration{0, -time.Second, time.Duration(math.MaxInt64)} {
		if _, err := st.CreateSessionWithTTL("runs", "alpha", ttl); !errors.Is(err, elena.ErrInvalidTTL) {
			t.Errorf("CreateSessionWithTTL(ttl = %v) err = %v, want ErrInvalidTTL", ttl, err)
		}
	}
}

// A session rejected for an invalid TTL must not exist afterwards: the
// name stays free for a well-formed create.
func TestCreateSessionWithTTLRejectsInvalidTTLLeavesNoSession(t *testing.T) {
	st := openStore(t)

	if _, err := st.CreateSessionWithTTL("runs", "alpha", 0); !errors.Is(err, elena.ErrInvalidTTL) {
		t.Fatalf("CreateSessionWithTTL(ttl = 0) err = %v, want ErrInvalidTTL", err)
	}
	sess := createSession(t, st, "runs", "alpha")
	if seq, err := sess.Append([]byte("event")); err != nil || seq != 1 {
		t.Fatalf("Append() after rejected create = (%d, %v), want (1, nil)", seq, err)
	}
}

// The deadline of a timed session is decided by what the store holds on
// disk, not by the process that attached to it: reopening a directory
// and attaching to the session again — with any TTL argument — must
// continue the stored session under its original deadline, and once
// that deadline passes the session must read as gone across the
// Close/Open boundary too.
func TestTimedSessionDeadlineSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	const ttl = time.Second

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	first := createTimedSession(t, st, "runs", "alpha", ttl)
	for i := 0; i < 2; i++ {
		mustAppend(t, first, "event")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	// Attach with a much larger TTL: the stored session, not this call,
	// must govern — the two stored events read back before the deadline.
	second := createTimedSession(t, reopened, "runs", "alpha", 30*time.Second)
	events, err := second.Events()
	if err != nil {
		t.Fatalf("Events() before the deadline failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Events() before the deadline returned %d events, want 2", len(events))
	}

	// Past the original deadline — though far short of the 30s this
	// attach call asked for — the session must be gone.
	waitForTTLElapse(ttl - 300*time.Millisecond)
	events, err = second.Events()
	if err != nil {
		t.Fatalf("Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the deadline returned %d events, want 0", len(events))
	}
	if _, err := second.Event(1); !errors.Is(err, elena.ErrEventNotFound) {
		t.Errorf("Event(1) after the deadline err = %v, want ErrEventNotFound", err)
	}
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

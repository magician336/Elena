package elena_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/magician336/elena"
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

// A create call that asks for a lifecycle form different from the one
// the stored session already has must fail with ErrSessionExists, in
// either direction, and leave the existing session untouched — the
// session's lifecycle is a property of the stored session, not of the
// handle a call happens to want.
func TestCreateSessionLifecycleFormConflictRejected(t *testing.T) {
	st := openStore(t)

	keeper := createSession(t, st, "runs", "keeper")
	if _, err := st.CreateSessionWithTTL("runs", "keeper", time.Minute); !errors.Is(err, elena.ErrSessionExists) {
		t.Errorf("CreateSessionWithTTL over a perpetual session err = %v, want ErrSessionExists", err)
	}

	temp := createTimedSession(t, st, "runs", "temp", time.Minute)
	if _, err := st.CreateSession("runs", "temp"); !errors.Is(err, elena.ErrSessionExists) {
		t.Errorf("CreateSession over a timed session err = %v, want ErrSessionExists", err)
	}

	// The rejected calls must have disturbed neither session.
	if seq, err := keeper.Append([]byte("kept")); err != nil || seq != 1 {
		t.Errorf("perpetual Append() after rejected creates = (%d, %v), want (1, nil)", seq, err)
	}
	if seq, err := temp.Append([]byte("temporary")); err != nil || seq != 1 {
		t.Errorf("timed Append() after rejected creates = (%d, %v), want (1, nil)", seq, err)
	}
}

// The form conflict must hold across Close and Open too: a stored timed
// session is not turned perpetual by reopening and attaching without a
// TTL.
func TestCreateSessionLifecycleFormConflictSurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	createTimedSession(t, st, "runs", "temp", time.Minute)
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.CreateSession("runs", "temp"); !errors.Is(err, elena.ErrSessionExists) {
		t.Fatalf("CreateSession over a stored timed session err = %v, want ErrSessionExists", err)
	}
}

// Creating an existing timed session with a TTL is idempotent: it
// returns a handle to the stored session — one shared trajectory, one
// continuing sequence — and the ttl argument is consulted only when the
// session is first created, so the first call's deadline governs.
func TestCreateSessionWithTTLOnTimedSessionIsIdempotent(t *testing.T) {
	st := openStore(t)
	const ttl = 500 * time.Millisecond
	first := createTimedSession(t, st, "runs", "alpha", ttl)
	mustAppend(t, first, "event")

	second := createTimedSession(t, st, "runs", "alpha", 30*time.Second)
	if seq, err := second.Append([]byte("event")); err != nil || seq != 2 {
		t.Fatalf("Append() through the second handle = (%d, %v), want (2, nil)", seq, err)
	}

	waitForTTLElapse(ttl)
	events, err := second.Events()
	if err != nil {
		t.Fatalf("Events() after the first call's deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the first call's deadline returned %d events, want 0 — the first deadline must govern", len(events))
	}
}

// Appending to a session whose TTL has elapsed must fail with
// ErrSessionEnded: the session — metadata and events — already reads as
// if never written, and a write now would resurrect half of it.
func TestAppendAfterTTLElapsesReturnsErrSessionEnded(t *testing.T) {
	st := openStore(t)
	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "alpha", ttl)
	mustAppend(t, sess, "event")

	waitForTTLElapse(ttl)
	if _, err := sess.Append([]byte("event")); !errors.Is(err, elena.ErrSessionEnded) {
		t.Fatalf("Append() after the deadline err = %v, want ErrSessionEnded", err)
	}
}

// A session whose TTL has elapsed reads as if never written, so its
// name is free: creating it again — timed or perpetual — starts a fresh
// session in the same name, with a fresh trajectory.
func TestTimedSessionCanBeCreatedAnewAfterItsTTLElapses(t *testing.T) {
	st := openStore(t)
	const firstTTL = 300 * time.Millisecond
	first := createTimedSession(t, st, "runs", "alpha", firstTTL)
	mustAppend(t, first, "old")

	waitForTTLElapse(firstTTL)

	reborn := createTimedSession(t, st, "runs", "alpha", time.Minute)
	if seq, err := reborn.Append([]byte("new")); err != nil || seq != 1 {
		t.Fatalf("Append() to the recreated session = (%d, %v), want (1, nil)", seq, err)
	}
	events, err := reborn.Events()
	if err != nil {
		t.Fatalf("Events() of the recreated session failed: %v", err)
	}
	if len(events) != 1 || string(events[0].Payload) != "new" {
		t.Fatalf("Events() of the recreated session = %v, want one event with payload \"new\"", events)
	}
}

// The fresh start holds in the perpetual form too: recreating a name
// whose timed session elapsed as a perpetual session keeps the new
// trajectory visible for good, not just until the old deadline.
func TestTimedSessionCanBeRecreatedPerpetualAfterItsTTLElapses(t *testing.T) {
	st := openStore(t)
	const ttl = 300 * time.Millisecond
	createTimedSession(t, st, "runs", "alpha", ttl)

	waitForTTLElapse(ttl)

	reborn := createSession(t, st, "runs", "alpha")
	mustAppend(t, reborn, "kept")
	// Well past the old deadline, the perpetual trajectory stays.
	time.Sleep(400 * time.Millisecond)
	events, err := reborn.Events()
	if err != nil {
		t.Fatalf("Events() of the perpetual recreation failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events() of the perpetual recreation returned %d events, want 1", len(events))
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

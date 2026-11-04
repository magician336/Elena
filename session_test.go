package elena_test

import (
	"errors"
	"strings"
	"testing"

	"elena"
)

// openStore opens a store in a fresh temporary directory and closes it
// when the test ends.
func openStore(t *testing.T) *elena.Store {
	t.Helper()
	st, err := elena.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })
	return st
}

// createSession creates the session named by namespace and session and
// fails the test on error.
func createSession(t *testing.T, st *elena.Store, namespace, session string) *elena.Session {
	t.Helper()
	sess, err := st.CreateSession(namespace, session)
	if err != nil {
		t.Fatalf("CreateSession(%q, %q) failed: %v", namespace, session, err)
	}
	return sess
}

// CreateSession must reject empty labels and labels beyond the
// documented limit with ErrInvalidLabel.
func TestCreateSessionRejectsInvalidLabels(t *testing.T) {
	st := openStore(t)

	tooLong := strings.Repeat("a", 481)
	cases := []struct {
		namespace, session string
	}{
		{"", "s"},
		{"ns", ""},
		{"", ""},
		{tooLong, "s"},
		{"ns", tooLong},
	}
	for _, tc := range cases {
		_, err := st.CreateSession(tc.namespace, tc.session)
		if !errors.Is(err, elena.ErrInvalidLabel) {
			t.Errorf("CreateSession(%q, %q) err = %v, want ErrInvalidLabel", tc.namespace, tc.session, err)
		}
	}
}

// Labels at the documented limit must be accepted: the encoded keys of
// two maximal labels stay inside the engine's key size limit.
func TestCreateSessionAcceptsMaximalLabels(t *testing.T) {
	st := openStore(t)

	max := strings.Repeat("a", 480)
	sess := createSession(t, st, max, max)
	if _, err := sess.Append([]byte("event")); err != nil {
		t.Fatalf("Append() on maximal labels failed: %v", err)
	}
}

// Append must hand out sequence numbers that start at 1 and increase
// by exactly one per event.
func TestAppendReturnsSequenceNumbersStartingAtOne(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	for want := uint64(1); want <= 3; want++ {
		seq, err := sess.Append([]byte("event"))
		if err != nil {
			t.Fatalf("Append() error: %v", err)
		}
		if seq != want {
			t.Fatalf("Append() seq = %d, want %d", seq, want)
		}
	}
}

// Repeated CreateSession calls for the same namespace and session must
// yield handles to one shared trajectory, not two.
func TestCreateSessionReturnsHandleSharingTrajectory(t *testing.T) {
	st := openStore(t)

	first := createSession(t, st, "runs", "alpha")
	second := createSession(t, st, "runs", "alpha")

	for _, tc := range []struct {
		handle *elena.Session
		want   uint64
	}{
		{first, 1},
		{second, 2},
		{first, 3},
	} {
		seq, err := tc.handle.Append([]byte("event"))
		if err != nil {
			t.Fatalf("Append() error: %v", err)
		}
		if seq != tc.want {
			t.Fatalf("Append() seq = %d, want %d", seq, tc.want)
		}
	}
}

// A failed append must leave no hole: the payload rejected here is
// beyond the storable size, so the store hands out no sequence number
// for it and the next append continues the sequence.
func TestAppendRejectsPayloadBeyondStorableSize(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	if _, err := sess.Append([]byte("kept")); err != nil {
		t.Fatalf("first Append() error: %v", err)
	}
	huge := make([]byte, 4<<20+64) // beyond the engine's default value limit
	_, err := sess.Append(huge)
	if err == nil {
		t.Fatal("Append() of an oversized payload must fail")
	}
	if errors.Is(err, elena.ErrClosed) {
		t.Fatalf("oversized payload must be rejected as too large, not closed: %v", err)
	}

	seq, err := sess.Append([]byte("kept"))
	if err != nil {
		t.Fatalf("Append() after rejection error: %v", err)
	}
	if seq != 2 {
		t.Fatalf("Append() after rejection seq = %d, want 2", seq)
	}
}

// After Close, appends must fail with an error wrapping ErrClosed.
func TestAppendAfterCloseReturnsErrClosed(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	if _, err := sess.Append([]byte("event")); err != nil {
		t.Fatalf("Append() before Close error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	if _, err := sess.Append([]byte("event")); !errors.Is(err, elena.ErrClosed) {
		t.Fatalf("Append() after Close err = %v, want ErrClosed", err)
	}
}

// Close followed by Open must preserve the stored trajectory and
// continue its sequence numbers without gaps or wrap-around: the
// counter is rebuilt from the trajectory's stored tail.
func TestReopenContinuesTrajectorySequence(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	first := createSession(t, st, "runs", "alpha")
	for i := 0; i < 3; i++ {
		if _, err := first.Append([]byte("event")); err != nil {
			t.Fatalf("Append() error: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	second := createSession(t, reopened, "runs", "alpha")
	for want := uint64(4); want <= 5; want++ {
		seq, err := second.Append([]byte("event"))
		if err != nil {
			t.Fatalf("Append() after reopen error: %v", err)
		}
		if seq != want {
			t.Fatalf("Append() after reopen seq = %d, want %d", seq, want)
		}
	}
}

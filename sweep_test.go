package elena_test

import (
	"errors"
	"testing"
	"time"

	"elena"
)

// waitForStats polls Stats until cond passes, failing the test when the
// condition never holds within a generous deadline. The background sweep
// acts asynchronously — a deadline elapses, the next period's check
// notices, the merge runs — so reclamation is observed by polling the
// same port the contrast assertions of the TTL ticket use: facade Stats.
func waitForStats(t *testing.T, st *elena.Store, what string, cond func(elena.Stats) bool) elena.Stats {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stats := st.Stats()
		if cond(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last Stats: %+v", what, stats)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A merge must physically reclaim the bytes of an elapsed session: the
// TTL ticket held that DataBytes, LiveBytes, and the entry counts stand
// exactly where they stood when the deadline passed — this test is that
// contrast assertion's other half. One Merge later, the whole session —
// metadata and events — is gone from the byte and index figures, and
// the session was already invisible before, so visibility does not move.
func TestMergeReclaimsElapsedSessionBytes(t *testing.T) {
	st := openStore(t)
	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}

	before := st.Stats()
	if before.DataBytes == 0 {
		t.Fatal("Stats().DataBytes = 0, want the stored session and its events to occupy bytes")
	}
	if before.LiveEntries < 4 { // the metadata entry plus three events
		t.Fatalf("Stats().LiveEntries = %d, want at least 4", before.LiveEntries)
	}

	waitForTTLElapse(ttl)

	if err := st.Merge(); err != nil {
		t.Fatalf("Merge() after the deadline failed: %v", err)
	}

	after := st.Stats()
	for _, tc := range []struct {
		name string
		got  uint64
	}{
		{"LiveEntries", after.LiveEntries},
		{"DataBytes", after.DataBytes},
		{"LiveBytes", after.LiveBytes},
		{"DeadBytes", after.DeadBytes},
	} {
		if tc.got != 0 {
			t.Errorf("Stats().%s after Merge() = %d, want 0 — the elapsed session's bytes must be reclaimed", tc.name, tc.got)
		}
	}
	if after.OpsMerge != before.OpsMerge+1 {
		t.Errorf("Stats().OpsMerge after Merge() = %d, want %d (one merge ran)", after.OpsMerge, before.OpsMerge+1)
	}

	// The merge reclaims bytes; it must not change visibility — the
	// session read as gone before the merge and still does.
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after Merge() failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after Merge() returned %d events, want 0", len(events))
	}
}

// With a configured sweep period, an elapsed session is reclaimed with
// no manual call at all: the sweep notices the deadline on its next
// check and the merge it triggers returns the bytes — the contrast
// assertion of the TTL ticket reversed through the background path this
// time. Further checks after the reclaim are no-ops — the sweep is
// idempotent, exactly one merge runs — and visibility was gone the
// moment the deadline passed, not when the sweep acted.
func TestSweepReclaimsElapsedSessionAutomatically(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepPeriod(25*time.Millisecond))
	if err != nil {
		t.Fatalf("Open(WithSweepPeriod) failed: %v", err)
	}
	defer closeStore(t, st)

	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}

	waitForStats(t, st, "the sweep to reclaim the elapsed session", func(s elena.Stats) bool {
		return s.LiveEntries == 0 && s.DataBytes == 0
	})
	merged := waitForStats(t, st, "the sweep's merge to be counted", func(s elena.Stats) bool {
		return s.OpsMerge >= 1
	})
	if merged.OpsMerge != 1 {
		t.Errorf("Stats().OpsMerge = %d, want exactly 1 — one signal, one merge", merged.OpsMerge)
	}

	// Idempotence: several more periods change nothing. The signal was
	// consumed, the bytes are back, and repeated sweeps have no side
	// effect to have.
	time.Sleep(150 * time.Millisecond)
	settled := st.Stats()
	for _, tc := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"LiveEntries", settled.LiveEntries, 0},
		{"DataBytes", settled.DataBytes, 0},
		{"LiveBytes", settled.LiveBytes, 0},
		{"DeadBytes", settled.DeadBytes, 0},
		{"OpsMerge", settled.OpsMerge, merged.OpsMerge},
	} {
		if tc.got != tc.want {
			t.Errorf("Stats().%s after further sweeps = %d, want %d (unchanged)", tc.name, tc.got, tc.want)
		}
	}

	// Visibility was gone at the deadline, before and regardless of the
	// sweep acting.
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the sweep failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the sweep returned %d events, want 0", len(events))
	}
	if _, err := sess.Event(1); !errors.Is(err, elena.ErrEventNotFound) {
		t.Errorf("Event(1) after the sweep err = %v, want ErrEventNotFound", err)
	}
	sessions, err := st.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after the sweep failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("Sessions() after the sweep returned %d sessions, want 0", len(sessions))
	}
}

// WithSweepPeriod must reject periods that cannot define a check
// interval — zero or negative — with ErrInvalidSweepPeriod, and hand
// back no store, leaving the directory free to open afterwards.
func TestOpenWithSweepPeriodRejectsInvalidPeriod(t *testing.T) {
	dir := t.TempDir()

	for _, period := range []time.Duration{0, -time.Second} {
		st, err := elena.Open(dir, elena.WithSweepPeriod(period))
		if !errors.Is(err, elena.ErrInvalidSweepPeriod) {
			t.Errorf("Open(WithSweepPeriod(%v)) err = %v, want ErrInvalidSweepPeriod", period, err)
		}
		if st != nil {
			closeStore(t, st)
			t.Error("Open must not return a store on failure")
		}
	}

	// The rejected opens must not have locked the directory.
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) after rejected opens failed: %v", dir, err)
	}
	closeStore(t, st)
}

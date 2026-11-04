package elena_test

import (
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

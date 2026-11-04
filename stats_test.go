package elena_test

import (
	"testing"
	"time"
)

// A session's TTL elapsing changes what readers can see, not what the
// disk holds: after the deadline the session and its events are gone
// from every read, yet the engine's byte and dead-entry figures stand
// exactly where they stood — the bytes of the session are still counted
// as live data until a merge reclaims them (the sweep's job, not the
// read path's). This is the dividing line between lazy visibility and
// physical reclamation.
func TestStatsShowsTTLElapseChangesVisibilityNotBytes(t *testing.T) {
	st := openStore(t)
	const ttl = 400 * time.Millisecond
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

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the deadline returned %d events, want 0", len(events))
	}

	after := st.Stats()
	for _, tc := range []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"DataBytes", after.DataBytes, before.DataBytes},
		{"LiveBytes", after.LiveBytes, before.LiveBytes},
		{"DeadBytes", after.DeadBytes, before.DeadBytes},
		{"DeadRatio", after.DeadRatio, before.DeadRatio},
		{"LiveEntries", after.LiveEntries, before.LiveEntries},
		{"DeadEntries", after.DeadEntries, before.DeadEntries},
	} {
		if tc.got != tc.want {
			t.Errorf("Stats().%s after the deadline = %v, want %v (unchanged)", tc.name, tc.got, tc.want)
		}
	}
}

// Stats must be an observation, not a mutation: reading it repeatedly
// returns a stable byte picture.
func TestStatsIsStableAcrossReads(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "event")

	first := st.Stats()
	time.Sleep(50 * time.Millisecond)
	second := st.Stats()
	if first.DataBytes != second.DataBytes || first.LiveEntries != second.LiveEntries {
		t.Errorf("Stats() drifted across reads: (%d, %d) then (%d, %d)",
			first.DataBytes, first.LiveEntries, second.DataBytes, second.LiveEntries)
	}
}

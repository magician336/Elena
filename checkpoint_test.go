package elena_test

import (
	"errors"
	"testing"
	"time"

	"elena"
)

// CreateCheckpoint must hand out IDs that increase with creation order,
// and Checkpoints must list those checkpoints — ID and creation time —
// in the same order. The list is storage, not memory: it survives
// Close and Open with the IDs and creation times intact.
func TestCreateCheckpointReturnsIDsListedInCreationOrderAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "event")

	windowStart := time.Now().Add(-time.Minute)
	first, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("first CreateCheckpoint() failed: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // keep creation times apart at any clock grain
	second, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("second CreateCheckpoint() failed: %v", err)
	}
	windowEnd := time.Now().Add(time.Minute)
	if first == 0 {
		t.Fatalf("first CreateCheckpoint() ID = 0, want a non-zero ID")
	}
	if second <= first {
		t.Fatalf("checkpoint IDs = (%d, %d), want strictly increasing", first, second)
	}

	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Fatalf("Checkpoints() returned %d checkpoints, want 2", len(checkpoints))
	}
	for i, cp := range checkpoints {
		want := first + uint64(i)
		if cp.ID != want {
			t.Errorf("Checkpoints()[%d].ID = %d, want %d", i, cp.ID, want)
		}
		if cp.CreatedAt.Before(windowStart) || cp.CreatedAt.After(windowEnd) {
			t.Errorf("Checkpoints()[%d].CreatedAt = %v, want within the test window", i, cp.CreatedAt)
		}
	}
	if !checkpoints[1].CreatedAt.After(checkpoints[0].CreatedAt) {
		t.Errorf("creation times = (%v, %v), want ascending with the IDs", checkpoints[0].CreatedAt, checkpoints[1].CreatedAt)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	reopenedCheckpoints, err := reopened.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after reopen failed: %v", err)
	}
	if len(reopenedCheckpoints) != 2 {
		t.Fatalf("Checkpoints() after reopen returned %d checkpoints, want 2", len(reopenedCheckpoints))
	}
	for i := range reopenedCheckpoints {
		if reopenedCheckpoints[i].ID != checkpoints[i].ID {
			t.Errorf("after reopen Checkpoints()[%d].ID = %d, want %d", i, reopenedCheckpoints[i].ID, checkpoints[i].ID)
		}
		if !reopenedCheckpoints[i].CreatedAt.Equal(checkpoints[i].CreatedAt) {
			t.Errorf("after reopen Checkpoints()[%d].CreatedAt = %v, want the stored %v", i, reopenedCheckpoints[i].CreatedAt, checkpoints[i].CreatedAt)
		}
	}
}

// After a Close and Open, Restore must reset the runtime view to the
// checkpoint's manifest: the sequence counter of every trajectory the
// checkpoint records rolls back to the recorded sequence number, so
// appends continue from the baseline and an appended event whose
// sequence number is already occupied on disk overwrites it — last
// write wins, the overwritten event's bytes turning into dead data that
// a merge will reclaim.
func TestRestoreAfterReopenContinuesFromBaselineSequence(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "baseline")
	}
	id, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint() failed: %v", err)
	}
	for i := 0; i < 2; i++ {
		mustAppend(t, sess, "post-baseline")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	// Without restore, the trajectory continues from the stored tail.
	fresh := createSession(t, reopened, "runs", "alpha")
	if seq, err := fresh.Append([]byte("no-restore")); err != nil || seq != 6 {
		t.Fatalf("Append() without restore = (%d, %v), want (6, nil)", seq, err)
	}
	if err := reopened.Restore(id); err != nil {
		t.Fatalf("Restore(%d) failed: %v", id, err)
	}

	// After restore the counter sits at the baseline: the next append
	// takes sequence 4 and overwrites the stored event 4 — last write
	// wins.
	restored := createSession(t, reopened, "runs", "alpha")
	events, err := restored.Events()
	if err != nil {
		t.Fatalf("Events() after restore failed: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("Events() after restore returned %d events, want 6 — restore hides nothing", len(events))
	}
	if seq, err := restored.Append([]byte("rewritten")); err != nil || seq != 4 {
		t.Fatalf("Append() after restore = (%d, %v), want (4, nil)", seq, err)
	}

	events, err = restored.Events()
	if err != nil {
		t.Fatalf("Events() after the overwriting append failed: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("Events() after the overwriting append returned %d events, want 6", len(events))
	}
	if string(events[3].Payload) != "rewritten" {
		t.Errorf("event 4 after the overwriting append payload = %q, want %q", events[3].Payload, "rewritten")
	}
	if string(events[4].Payload) != "post-baseline" {
		t.Errorf("event 5 payload = %q, want the still-visible %q", events[4].Payload, "post-baseline")
	}

	// The overwritten event's bytes are dead data now: no reader can
	// reach them, and Stats reports them as awaiting a merge.
	stats := reopened.Stats()
	if stats.DeadEntries == 0 || stats.DeadBytes == 0 {
		t.Errorf("after the overwriting append DeadEntries = %d, DeadBytes = %d, want both above 0", stats.DeadEntries, stats.DeadBytes)
	}
}

// Restore of a checkpoint the store does not hold must fail with
// ErrCheckpointNotFound and change nothing.
func TestRestoreUnknownCheckpointReturnsErrCheckpointNotFound(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "event")

	if err := st.Restore(999); !errors.Is(err, elena.ErrCheckpointNotFound) {
		t.Fatalf("Restore(999) err = %v, want ErrCheckpointNotFound", err)
	}
	if seq, err := sess.Append([]byte("event")); err != nil || seq != 2 {
		t.Fatalf("Append() after the rejected restore = (%d, %v), want (2, nil)", seq, err)
	}
}

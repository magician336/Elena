package elena_test

import (
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

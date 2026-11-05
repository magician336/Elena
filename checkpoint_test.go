package elena_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/magician336/elena"
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

// Restore is a baseline to recover to, not a time machine: a session
// created after the checkpoint stays on disk, stays listed, and its
// trajectory keeps running from its own stored tail — restore neither
// hides it nor rewinds it, because the manifest recorded nothing about
// it.
func TestRestoreLeavesPostBaselineSessionsVisibleAndRunning(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	baseline := createSession(t, st, "runs", "baseline")
	mustAppend(t, baseline, "event")
	id, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint() failed: %v", err)
	}

	// After the baseline point: a fresh session with events of its own.
	later := createSession(t, st, "runs", "later")
	for i := 0; i < 2; i++ {
		mustAppend(t, later, "post-baseline")
	}
	if err := st.Restore(id); err != nil {
		t.Fatalf("Restore(%d) failed: %v", id, err)
	}

	sessions, err := st.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after restore failed: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "baseline" || sessions[1].ID != "later" {
		t.Fatalf("Sessions() after restore = %v, want both baseline and later listed", sessions)
	}
	laterEvents, err := later.Events()
	if err != nil {
		t.Fatalf("Events() of the post-baseline session after restore failed: %v", err)
	}
	if len(laterEvents) != 2 {
		t.Fatalf("Events() of the post-baseline session after restore returned %d events, want 2", len(laterEvents))
	}
	if seq, err := later.Append([]byte("continues")); err != nil || seq != 3 {
		t.Fatalf("Append() of the post-baseline session after restore = (%d, %v), want (3, nil)", seq, err)
	}
}

// A restored timed session must elapse at the deadline the checkpoint
// recorded — the one its stored entries have carried since creation —
// not at one recomputed at restore time and not at whatever TTL a later
// attach call names. The gaps between the instants are chosen so a
// recomputed deadline would still be in the future when the original
// one has already passed.
func TestRestoreKeepsTimedSessionsOriginalDeadline(t *testing.T) {
	dir := t.TempDir()
	const ttl = 400 * time.Millisecond

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	created := time.Now()
	sess := createTimedSession(t, st, "runs", "alpha", ttl)
	mustAppend(t, sess, "event")
	id, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint() failed: %v", err)
	}
	// Roughly halfway into the TTL, past a quarter of it: any deadline
	// recomputed from here on outlives the original.
	time.Sleep(ttl / 2)
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)
	if err := reopened.Restore(id); err != nil {
		t.Fatalf("Restore(%d) failed: %v", id, err)
	}

	// Attaching with a much larger TTL must not renew either: the
	// restored deadline governs.
	restored := createTimedSession(t, reopened, "runs", "alpha", time.Hour)
	if seq, err := restored.Append([]byte("still-live")); err != nil || seq != 2 {
		t.Fatalf("Append() before the original deadline = (%d, %v), want (2, nil)", seq, err)
	}
	events, err := restored.Events()
	if err != nil {
		t.Fatalf("Events() before the original deadline failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Events() before the original deadline returned %d events, want 2", len(events))
	}

	// Past the original deadline — though far short of both the hour
	// this attach call asked for and any deadline recomputed at restore
	// time — the session must be gone.
	remaining := time.Until(created.Add(ttl + 150*time.Millisecond))
	time.Sleep(max(remaining, 0))
	events, err = restored.Events()
	if err != nil {
		t.Fatalf("Events() after the original deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the original deadline returned %d events, want 0 — the restored deadline, not a recomputed one, must govern", len(events))
	}
	if _, err := restored.Append([]byte("event")); !errors.Is(err, elena.ErrSessionEnded) {
		t.Errorf("Append() after the original deadline err = %v, want ErrSessionEnded", err)
	}
}

// A batch must be able to carry a group of events and a checkpoint in
// one atomic commit: one Commit makes the whole group and the baseline
// durable together, the checkpoint's manifest recording the group's own
// sequence numbers, and both readable after a reopen.
func TestBatchCommitsEventsAndCheckpointTogether(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "before-batch")

	batch := st.NewBatch()
	for _, payload := range []string{"batch-a", "batch-b"} {
		if err := batch.Append(sess, []byte(payload)); err != nil {
			t.Fatalf("batch Append(%q) failed: %v", payload, err)
		}
	}
	batch.Checkpoint()
	seqs, id, err := batch.Commit()
	if err != nil {
		t.Fatalf("batch Commit() failed: %v", err)
	}
	if len(seqs) != 2 || seqs[0] != 2 || seqs[1] != 3 {
		t.Fatalf("batch Commit() seqs = %v, want [2 3]", seqs)
	}
	if id == 0 {
		t.Fatal("batch Commit() checkpoint ID = 0, want the staged checkpoint's ID")
	}

	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != id {
		t.Fatalf("Checkpoints() = %v, want the committed checkpoint %d alone", checkpoints, id)
	}
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the batch failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Events() after the batch returned %d events, want 3", len(events))
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// The manifest recorded the batch's own events: restoring to it in a
	// fresh process continues right behind them.
	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)
	if err := reopened.Restore(id); err != nil {
		t.Fatalf("Restore(%d) failed: %v", id, err)
	}
	restored := createSession(t, reopened, "runs", "alpha")
	if seq, err := restored.Append([]byte("after-restore")); err != nil || seq != 4 {
		t.Fatalf("Append() after restore to the batch checkpoint = (%d, %v), want (4, nil)", seq, err)
	}
}

// A batch without a staged checkpoint commits its events alone: no
// checkpoint appears, and the returned checkpoint ID is zero.
func TestBatchWithoutCheckpointCommitsEventsAlone(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	batch := st.NewBatch()
	if err := batch.Append(sess, []byte("event")); err != nil {
		t.Fatalf("batch Append() failed: %v", err)
	}
	seqs, id, err := batch.Commit()
	if err != nil {
		t.Fatalf("batch Commit() failed: %v", err)
	}
	if len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("batch Commit() seqs = %v, want [1]", seqs)
	}
	if id != 0 {
		t.Fatalf("batch Commit() checkpoint ID = %d, want 0 for no staged checkpoint", id)
	}
	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("Checkpoints() after a checkpoint-less batch = %v, want none", checkpoints)
	}
}

// A commit attempted after Close must fail whole: no staged event
// becomes visible and no half checkpoint is left behind — the group and
// the baseline either survive together or do not exist at all.
func TestBatchCommitAfterCloseLeavesNothing(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	sess := createSession(t, st, "runs", "alpha")
	mustAppend(t, sess, "before-batch")

	batch := st.NewBatch()
	if err := batch.Append(sess, []byte("batch")); err != nil {
		t.Fatalf("batch Append() failed: %v", err)
	}
	batch.Checkpoint()
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	if _, _, err := batch.Commit(); !errors.Is(err, elena.ErrClosed) {
		t.Fatalf("batch Commit() after Close err = %v, want ErrClosed", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)
	checkpoints, err := reopened.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after the failed commit failed: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("Checkpoints() after the failed commit = %v, want none — no half checkpoint", checkpoints)
	}
	sess2 := createSession(t, reopened, "runs", "alpha")
	events, err := sess2.Events()
	if err != nil {
		t.Fatalf("Events() after the failed commit failed: %v", err)
	}
	if len(events) != 1 || string(events[0].Payload) != "before-batch" {
		t.Fatalf("Events() after the failed commit = %v, want the one pre-batch event alone", events)
	}
}

// A session whose TTL elapses between staging and commit must fail the
// whole commit with ErrSessionEnded: none of the batch's events — not
// even those of other, still-live sessions — may become visible, and no
// checkpoint may appear. The aborted commit allocates no sequence
// numbers, so the live session's next append continues without a hole.
func TestBatchCommitAfterTTLElapseFailsWhole(t *testing.T) {
	st := openStore(t)
	const ttl = 300 * time.Millisecond

	keeper := createSession(t, st, "runs", "keeper")
	expiring := createTimedSession(t, st, "runs", "expiring", ttl)
	mustAppend(t, keeper, "before-batch")
	mustAppend(t, expiring, "before-batch")

	batch := st.NewBatch()
	if err := batch.Append(keeper, []byte("batch")); err != nil {
		t.Fatalf("batch Append() to the perpetual session failed: %v", err)
	}
	if err := batch.Append(expiring, []byte("batch")); err != nil {
		t.Fatalf("batch Append() to the timed session failed: %v", err)
	}
	batch.Checkpoint()
	waitForTTLElapse(ttl)

	if _, _, err := batch.Commit(); !errors.Is(err, elena.ErrSessionEnded) {
		t.Fatalf("batch Commit() after the TTL elapsed err = %v, want ErrSessionEnded", err)
	}
	events, err := keeper.Events()
	if err != nil {
		t.Fatalf("Events() of the perpetual session failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events() of the perpetual session after the aborted commit returned %d events, want 1", len(events))
	}
	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("Checkpoints() after the aborted commit = %v, want none", checkpoints)
	}
	if seq, err := keeper.Append([]byte("no-hole")); err != nil || seq != 2 {
		t.Fatalf("Append() after the aborted commit = (%d, %v), want (2, nil)", seq, err)
	}
}

// Staging failures must leave nothing behind: an append rejected at
// staging allocates no sequence number, so the trajectory continues
// without a hole, and the store holds no checkpoint.
func TestBatchStagingFailuresLeaveNothingBehind(t *testing.T) {
	st := openStore(t)
	other := openStore(t)
	sess := createSession(t, st, "runs", "alpha")
	foreign := createSession(t, other, "runs", "alpha")

	if err := st.NewBatch().Append(foreign, []byte("event")); err == nil {
		t.Error("batch Append() of another store's session handle must fail")
	}
	batch := st.NewBatch()
	if err := batch.Append(sess, []byte("kept")); err != nil {
		t.Fatalf("batch Append() after the rejection failed: %v", err)
	}
	if _, id, err := batch.Commit(); err != nil || id != 0 {
		t.Fatalf("batch Commit() = (id %d, %v), want (0, nil)", id, err)
	}
	if seq, err := sess.Append([]byte("no-hole")); err != nil || seq != 2 {
		t.Fatalf("Append() after the batch = (%d, %v), want (2, nil) — staging must allocate nothing", seq, err)
	}
	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("Checkpoints() = %v, want none", checkpoints)
	}
}

// A payload beyond the storable size becomes an error only at commit —
// the size verdict is the engine's — and the commit must fail whole:
// no event of the batch visible, no checkpoint left, and no sequence
// number burned.
func TestBatchCommitRejectingOversizedPayloadFailsWhole(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "alpha")

	batch := st.NewBatch()
	if err := batch.Append(sess, []byte("would-have-been-first")); err != nil {
		t.Fatalf("batch Append() before the size verdict failed: %v", err)
	}
	if err := batch.Append(sess, make([]byte, 4<<20+64)); err != nil {
		t.Fatalf("batch Append() of the oversized payload must stage, not fail early: %v", err)
	}
	batch.Checkpoint()
	if _, _, err := batch.Commit(); err == nil || errors.Is(err, elena.ErrClosed) {
		t.Fatalf("batch Commit() with an oversized payload err = %v, want a size error", err)
	}

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the failed commit failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the failed commit returned %d events, want 0", len(events))
	}
	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("Checkpoints() after the failed commit = %v, want none", checkpoints)
	}
	if seq, err := sess.Append([]byte("no-hole")); err != nil || seq != 1 {
		t.Fatalf("Append() after the failed commit = (%d, %v), want (1, nil)", seq, err)
	}
}

// Events appended to a timed session through a batch must carry the
// session's deadline like any other event: the whole trajectory turns
// invisible together when the TTL elapses, while the checkpoint —
// stored without a deadline — remains listable.
func TestBatchCarriesTimedSessionsDeadline(t *testing.T) {
	st := openStore(t)
	const ttl = 400 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "alpha", ttl)

	batch := st.NewBatch()
	for _, payload := range []string{"first", "second"} {
		if err := batch.Append(sess, []byte(payload)); err != nil {
			t.Fatalf("batch Append(%q) failed: %v", payload, err)
		}
	}
	batch.Checkpoint()
	if _, id, err := batch.Commit(); err != nil || id == 0 {
		t.Fatalf("batch Commit() = (id %d, %v), want a committed checkpoint", id, err)
	}

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() before the deadline failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Events() before the deadline returned %d events, want 2", len(events))
	}

	waitForTTLElapse(ttl)
	events, err = sess.Events()
	if err != nil {
		t.Fatalf("Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the deadline returned %d events, want 0 — batch events must carry the session deadline", len(events))
	}
	checkpoints, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after the deadline failed: %v", err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("Checkpoints() after the deadline returned %d checkpoints, want 1 — the checkpoint itself has no deadline", len(checkpoints))
	}
}

// Checkpoint creation, listing, batch commits, and plain appends must
// run concurrently without losing events or inventing duplicate
// checkpoint IDs: every checkpoint lists alone under its own ID, and
// the trajectory ends up holding every appended event exactly once.
func TestConcurrentCheckpointsBatchesAndAppends(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "busy")
	mustAppend(t, sess, "seed")

	const appenders = 4
	const perAppender = 10
	const checkpoints = 6

	var wg sync.WaitGroup
	for g := 0; g < appenders; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perAppender; i++ {
				if _, err := sess.Append([]byte(fmt.Sprintf("g%d-%d", g, i))); err != nil {
					t.Errorf("Append() error: %v", err)
					return
				}
			}
		}(g)
	}
	for c := 0; c < checkpoints; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			if c%2 == 0 {
				if _, err := st.CreateCheckpoint(); err != nil {
					t.Errorf("CreateCheckpoint() error: %v", err)
					return
				}
				return
			}
			batch := st.NewBatch()
			if err := batch.Append(sess, []byte("batch")); err != nil {
				t.Errorf("batch Append() error: %v", err)
				return
			}
			batch.Checkpoint()
			if _, _, err := batch.Commit(); err != nil {
				t.Errorf("batch Commit() error: %v", err)
				return
			}
		}(c)
	}
	wg.Wait()

	listed, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() failed: %v", err)
	}
	if len(listed) != checkpoints {
		t.Fatalf("Checkpoints() returned %d checkpoints, want %d", len(listed), checkpoints)
	}
	for i, cp := range listed {
		if cp.ID != uint64(i+1) {
			t.Errorf("Checkpoints()[%d].ID = %d, want %d — every checkpoint must list alone under its own ID", i, cp.ID, i+1)
		}
	}

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() failed: %v", err)
	}
	want := 1 + appenders*perAppender + checkpoints/2
	if len(events) != want {
		t.Fatalf("Events() returned %d events, want %d", len(events), want)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("Events() position %d carries Seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

// Checkpoint must expose the manifest one checkpoint recorded: every
// trajectory of the baseline — namespace, session, the reached sequence
// number, the deadline — in manifest order. The reached sequence number
// is the baseline in library terms: EventsUpTo(ReachedSeq) reads
// exactly what the checkpoint saw, whatever was appended after it. A
// stored manifest never changes: a checkpoint read after later appends
// and later checkpoints still reports the numbers it was created with.
func TestCheckpointReturnsTheRecordedBaselines(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	alpha := createSession(t, st, "runs", "alpha")
	mustAppend(t, alpha, "one")
	mustAppend(t, alpha, "two")
	mustAppend(t, alpha, "three")

	ttl := time.Hour
	timedWindowStart := time.Now()
	timed, err := st.CreateSessionWithTTL("runs", "timed", ttl)
	if err != nil {
		t.Fatalf("CreateSessionWithTTL(runs, timed) failed: %v", err)
	}
	if _, err := timed.Append([]byte("tick")); err != nil {
		t.Fatalf("timed Append failed: %v", err)
	}
	timedWindowEnd := time.Now()

	windowStart := time.Now()
	cp, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint() failed: %v", err)
	}
	windowEnd := time.Now()
	manifest, err := st.Checkpoint(cp)
	if err != nil {
		t.Fatalf("Checkpoint(%d) failed: %v", cp, err)
	}
	if manifest.ID != cp {
		t.Errorf("Checkpoint(%d).ID = %d, want %d", cp, manifest.ID, cp)
	}
	if manifest.CreatedAt.Before(windowStart) || manifest.CreatedAt.After(windowEnd) {
		t.Errorf("Checkpoint CreatedAt = %v, want within the test window", manifest.CreatedAt)
	}
	if len(manifest.Trajectories) != 2 {
		t.Fatalf("Checkpoint Trajectories = %d entries, want 2: %+v", len(manifest.Trajectories), manifest.Trajectories)
	}
	first, second := manifest.Trajectories[0], manifest.Trajectories[1]
	if first.Namespace != "runs" || first.Session != "alpha" {
		t.Errorf("Trajectories[0] = %s/%s, want runs/alpha in manifest order", first.Namespace, first.Session)
	}
	if first.ReachedSeq != 3 {
		t.Errorf("Trajectories[0].ReachedSeq = %d, want 3 — the baseline covers three events", first.ReachedSeq)
	}
	if !first.Deadline.IsZero() {
		t.Errorf("Trajectories[0].Deadline = %v, want the zero time of a perpetual session", first.Deadline)
	}
	if second.Namespace != "runs" || second.Session != "timed" {
		t.Errorf("Trajectories[1] = %s/%s, want runs/timed", second.Namespace, second.Session)
	}
	if second.ReachedSeq != 1 {
		t.Errorf("Trajectories[1].ReachedSeq = %d, want 1", second.ReachedSeq)
	}
	if second.Deadline.Before(timedWindowStart) || second.Deadline.After(timedWindowEnd.Add(ttl)) {
		t.Errorf("Trajectories[1].Deadline = %v, want between creation and creation+ttl", second.Deadline)
	}

	// Past the baseline: one more event, a second checkpoint. The first
	// manifest must still report the numbers it was created with, and
	// the second must record the new tail.
	mustAppend(t, alpha, "four")
	cp2, err := st.CreateCheckpoint()
	if err != nil {
		t.Fatalf("second CreateCheckpoint() failed: %v", err)
	}
	again, err := st.Checkpoint(cp)
	if err != nil {
		t.Fatalf("Checkpoint(%d) after later appends failed: %v", cp, err)
	}
	if got := again.Trajectories[0].ReachedSeq; got != 3 {
		t.Errorf("first manifest ReachedSeq after later appends = %d, want 3 — a stored manifest never changes", got)
	}
	later, err := st.Checkpoint(cp2)
	if err != nil {
		t.Fatalf("Checkpoint(%d) failed: %v", cp2, err)
	}
	if got := later.Trajectories[0].ReachedSeq; got != 4 {
		t.Errorf("second manifest ReachedSeq = %d, want 4", got)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)
	reopenedManifest, err := reopened.Checkpoint(cp)
	if err != nil {
		t.Fatalf("Checkpoint(%d) after reopen failed: %v", cp, err)
	}
	if len(reopenedManifest.Trajectories) != 2 || reopenedManifest.Trajectories[0].ReachedSeq != 3 {
		t.Errorf("Checkpoint(%d) after reopen = %+v, want the same manifest", cp, reopenedManifest.Trajectories)
	}
}

// Checkpoint must fail with ErrCheckpointNotFound for an ID nothing
// stored — and fail alone, changing nothing.
func TestCheckpointUnknownIDFailsAlone(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, st)

	if _, err := st.Checkpoint(99); !errors.Is(err, elena.ErrCheckpointNotFound) {
		t.Errorf("Checkpoint(99) err = %v, want ErrCheckpointNotFound", err)
	}
}

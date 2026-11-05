package main

import (
	"testing"
	"time"
)

// TestCheckpointStoryRunsTheScript drives the checkpoint story through
// the public library API against a real store directory and asserts
// the whole chain: two pre-baseline appends, one batch whose three
// events and checkpoint commit atomically, two post-baseline appends,
// a close standing in for the interruption, a reopen that lists the
// stored checkpoint, a restore to the baseline, a resume whose append
// continues at the baseline's next sequence number, and the closing
// contrast — the point-in-time read stops at the baseline while the
// full read still shows every event on disk.
func TestCheckpointStoryRunsTheScript(t *testing.T) {
	clk := newFakeClock()
	clk.Advance(time.Hour)

	story, err := checkpointStory(t.TempDir(), clk)
	if err != nil {
		t.Fatalf("checkpointStory: %v", err)
	}

	if story.Namespace != "runs" || story.Session != "rescue" {
		t.Fatalf("namespace/session = %q/%q, want runs/rescue", story.Namespace, story.Session)
	}
	if story.CreatedAt != "2026-01-05T10:00:00Z" {
		t.Fatalf("created_at = %s, want 2026-01-05T10:00:00Z", story.CreatedAt)
	}

	if len(story.PreBaseline) != 2 || story.PreBaseline[0].Seq != 1 || story.PreBaseline[1].Seq != 2 {
		t.Fatalf("pre-baseline events = %+v, want seqs 1 and 2", story.PreBaseline)
	}
	if story.PreBaseline[1].At != "2026-01-05T10:00:14Z" {
		t.Fatalf("second pre-baseline event at %s, want 2026-01-05T10:00:14Z", story.PreBaseline[1].At)
	}

	if len(story.Batch.Events) != 3 {
		t.Fatalf("batch events = %+v, want 3", story.Batch.Events)
	}
	for i, ev := range story.Batch.Events {
		if ev.Seq != uint64(3+i) {
			t.Fatalf("batch event %d has seq %d, want %d", i, ev.Seq, 3+i)
		}
	}
	if story.Batch.CheckpointID != 1 {
		t.Fatalf("checkpoint id = %d, want 1", story.Batch.CheckpointID)
	}
	if story.Batch.CommittedAt != "2026-01-05T10:00:21Z" {
		t.Fatalf("batch committed at %s, want 2026-01-05T10:00:21Z", story.Batch.CommittedAt)
	}

	if len(story.PostBaseline) != 2 || story.PostBaseline[0].Seq != 6 || story.PostBaseline[1].Seq != 7 {
		t.Fatalf("post-baseline events = %+v, want seqs 6 and 7", story.PostBaseline)
	}

	if story.Interruption.ClosedAt != "2026-01-05T10:00:42Z" {
		t.Fatalf("closed at %s, want 2026-01-05T10:00:42Z", story.Interruption.ClosedAt)
	}

	if story.Reopen.ReopenedAt != "2026-01-05T10:00:49Z" {
		t.Fatalf("reopened at %s, want 2026-01-05T10:00:49Z", story.Reopen.ReopenedAt)
	}
	if len(story.Reopen.Checkpoints) != 1 || story.Reopen.Checkpoints[0].ID != 1 {
		t.Fatalf("listed checkpoints = %+v, want exactly checkpoint 1", story.Reopen.Checkpoints)
	}
	if story.Reopen.Checkpoints[0].CreatedAt != "2026-01-05T10:00:21Z" {
		t.Fatalf("checkpoint listed with created-at %s, want the commit reading 2026-01-05T10:00:21Z",
			story.Reopen.Checkpoints[0].CreatedAt)
	}

	if story.Restore.CheckpointID != 1 || story.Restore.BaselineSeq != 5 {
		t.Fatalf("restore = %+v, want checkpoint 1 at baseline seq 5", story.Restore)
	}
	if story.Restore.RestoredAt != "2026-01-05T10:00:56Z" {
		t.Fatalf("restored at %s, want 2026-01-05T10:00:56Z", story.Restore.RestoredAt)
	}

	if story.Resume.Event.Seq != 6 {
		t.Fatalf("resume event seq = %d, want 6 — the baseline's next number", story.Resume.Event.Seq)
	}
	if story.Resume.OverwritesSeq != 6 {
		t.Fatalf("resume overwrites seq = %d, want 6 — last write wins", story.Resume.OverwritesSeq)
	}
	if story.Resume.Event.At != "2026-01-05T10:01:03Z" {
		t.Fatalf("resume event at %s, want 2026-01-05T10:01:03Z", story.Resume.Event.At)
	}

	if story.After.PointInTime.Seq != 5 || story.After.PointInTime.Returned != 5 {
		t.Fatalf("after point-in-time = %+v, want seq 5 returning 5 events", story.After.PointInTime)
	}
	if story.After.FullRead.Count != 7 {
		t.Fatalf("after full read = %d events, want 7 — data past the baseline is still on disk",
			story.After.FullRead.Count)
	}
}

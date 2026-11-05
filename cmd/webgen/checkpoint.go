package main

import (
	"fmt"

	"elena"
)

// The checkpoint story's fixed script: the payloads are constants —
// the seed drives the trajectory act; this act's point is the
// structure (baseline, interruption, restore, resume), and a fixed
// script tells it most directly.
const (
	checkpointNamespace = "runs"
	checkpointSession   = "rescue"
	resumePayload       = "resumed after restore"
	baselineLastIndex   = 2 // the third batch event: the baseline is its seq
)

var (
	preBaselinePayloads  = []string{"task opened", "tool result: schema loaded"}
	batchPayloads        = []string{"tool result: page fetched", "tool result: rows decoded", "plan revised: 3 steps"}
	postBaselinePayloads = []string{"interim answer stored", "final answer stored"}
)

// appendAll appends each payload through the session handle and
// returns the events as the page shows them, one injected-clock tick
// per append.
func appendAll(sess *elena.Session, clk *fakeClock, payloads []string) ([]StoryEvent, error) {
	events := make([]StoryEvent, 0, len(payloads))
	for _, payload := range payloads {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return nil, fmt.Errorf("append %q: %w", payload, err)
		}
		events = append(events, StoryEvent{Seq: seq, At: clk.tick(), Payload: payload})
	}
	return events, nil
}

// commitBaseline stages the batch's three events plus one checkpoint
// and commits them as a single atomic unit, returning the view.
func commitBaseline(store *elena.Store, sess *elena.Session, clk *fakeClock) (BatchView, error) {
	batch := store.NewBatch()
	for _, payload := range batchPayloads {
		if err := batch.Append(sess, []byte(payload)); err != nil {
			return BatchView{}, fmt.Errorf("batch append %q: %w", payload, err)
		}
	}
	batch.Checkpoint()
	seqs, cpID, err := batch.Commit()
	if err != nil {
		return BatchView{}, fmt.Errorf("batch commit: %w", err)
	}
	committedAt := clk.tick() // one reading for the whole atomic unit
	events := make([]StoryEvent, len(seqs))
	for i, seq := range seqs {
		events[i] = StoryEvent{Seq: seq, At: committedAt, Payload: batchPayloads[i]}
	}
	return BatchView{Events: events, CheckpointID: cpID, CommittedAt: committedAt}, nil
}

// checkpointStory narrates the second act against a real directory
// through the public library API. The close mid-story is the
// interruption stand-in; the timestamps are the injected clock's
// readings throughout.
func checkpointStory(dir string, clk *fakeClock) (story CheckpointStory, err error) {
	store, err := elena.Open(dir)
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("open %q: %w", dir, err)
	}
	defer func() { _ = store.Close() }() // best effort; Close is idempotent

	sess, err := store.CreateSession(checkpointNamespace, checkpointSession)
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("session create: %w", err)
	}
	story = CheckpointStory{
		Namespace: checkpointNamespace,
		Session:   checkpointSession,
		CreatedAt: clk.Now().UTC().Format(timestampFormat),
	}

	if story.PreBaseline, err = appendAll(sess, clk, preBaselinePayloads); err != nil {
		return CheckpointStory{}, err
	}
	if story.Batch, err = commitBaseline(store, sess, clk); err != nil {
		return CheckpointStory{}, err
	}
	if story.PostBaseline, err = appendAll(sess, clk, postBaselinePayloads); err != nil {
		return CheckpointStory{}, err
	}

	if err := store.Close(); err != nil {
		return CheckpointStory{}, fmt.Errorf("close (interruption): %w", err)
	}
	story.Interruption = InterruptView{ClosedAt: clk.tick()}

	store, err = elena.Open(dir)
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("reopen %q: %w", dir, err)
	}
	listed, err := store.Checkpoints()
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("checkpoints listing: %w", err)
	}
	story.Reopen = ReopenView{ReopenedAt: clk.tick()}
	for _, cp := range listed {
		story.Reopen.Checkpoints = append(story.Reopen.Checkpoints,
			CheckpointView{ID: cp.ID, CreatedAt: story.Batch.CommittedAt})
	}

	baselineSeq := story.Batch.Events[baselineLastIndex].Seq
	if err := store.Restore(story.Batch.CheckpointID); err != nil {
		return CheckpointStory{}, fmt.Errorf("restore: %w", err)
	}
	story.Restore = RestoreView{
		RestoredAt: clk.tick(), CheckpointID: story.Batch.CheckpointID, BaselineSeq: baselineSeq,
	}

	sess, err = store.CreateSession(checkpointNamespace, checkpointSession)
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("session resume: %w", err)
	}
	seq, err := sess.Append([]byte(resumePayload))
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("append %q: %w", resumePayload, err)
	}
	story.Resume = ResumeView{
		Event:         StoryEvent{Seq: seq, At: clk.tick(), Payload: resumePayload},
		OverwritesSeq: seq,
	}

	past, err := sess.EventsUpTo(baselineSeq)
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("point-in-time read to seq %d: %w", baselineSeq, err)
	}
	full, err := sess.Events()
	if err != nil {
		return CheckpointStory{}, fmt.Errorf("full read: %w", err)
	}
	story.After = AfterView{
		PointInTime: PointInTimeView{Seq: baselineSeq, At: clk.tick(), Returned: len(past)},
		FullRead:    FullReadView{At: clk.Now().UTC().Format(timestampFormat), Count: len(full)},
	}
	return story, nil
}

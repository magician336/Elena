package elena

import (
	"time"

	"elena/internal/semantic"
)

// ErrCheckpointNotFound is wrapped into the error Restore returns when
// the store holds no checkpoint with the requested ID.
var ErrCheckpointNotFound = semantic.ErrCheckpointNotFound

// CheckpointInfo describes one stored checkpoint: its ID and when it
// was created.
type CheckpointInfo struct {
	// ID identifies the checkpoint; Restore takes it back.
	ID uint64

	// CreatedAt is when the checkpoint was created, as stored with the
	// checkpoint — the same instant after every reopen.
	CreatedAt time.Time
}

// CreateCheckpoint records a manifest of the whole store as one
// checkpoint: for every live session, the trajectory's reached sequence
// number and the session's deadline, plus the checkpoint's creation
// time. The manifest lands durably in a single engine batch — one
// append, one fdatasync — and is ordinary stored data: it can be listed
// and restored across Close and Open. It returns the checkpoint's ID.
func (s *Store) CreateCheckpoint() (uint64, error) {
	return s.store.CreateCheckpoint()
}

// Checkpoints returns every stored checkpoint, in creation order.
func (s *Store) Checkpoints() ([]CheckpointInfo, error) {
	inner, err := s.store.Checkpoints()
	if err != nil {
		return nil, err
	}
	checkpoints := make([]CheckpointInfo, len(inner))
	for i, info := range inner {
		checkpoints[i] = CheckpointInfo{ID: info.ID, CreatedAt: info.CreatedAt}
	}
	return checkpoints, nil
}

// TrajectoryBaseline is one trajectory's line of a checkpoint's
// manifest: the trajectory's identity and the baseline the checkpoint
// recorded for it — the sequence number a point-in-time read stops at
// to see exactly what the checkpoint saw.
type TrajectoryBaseline struct {
	// Namespace is the trajectory's namespace.
	Namespace string

	// Session is the ID of the session owning the trajectory.
	Session string

	// ReachedSeq is the sequence number the checkpoint recorded for the
	// trajectory: the last event the baseline covers. EventsUpTo(
	// ReachedSeq) reads the baseline's own timeline; events appended
	// after the checkpoint stay on disk and read past it.
	ReachedSeq uint64

	// Deadline is the session deadline the manifest recorded — the zero
	// time marks a perpetual session.
	Deadline time.Time
}

// CheckpointManifest is one checkpoint in full: its ID, its creation
// time, and every trajectory's recorded baseline.
type CheckpointManifest struct {
	// ID identifies the checkpoint; Restore takes it back.
	ID uint64

	// CreatedAt is when the checkpoint was created, as stored with the
	// checkpoint — the same instant after every reopen.
	CreatedAt time.Time

	// Trajectories lists one baseline per session the manifest recorded,
	// in manifest order — the deterministic key order of the encoding.
	Trajectories []TrajectoryBaseline
}

// Checkpoint returns the manifest one stored checkpoint recorded: the
// creation-time view of every trajectory's reached sequence number and
// every session's deadline. A stored manifest never changes — reading
// it after later appends and later checkpoints reports the numbers it
// was created with, so the baseline it names is a stable fact on disk.
// An unknown ID fails with an error wrapping ErrCheckpointNotFound.
func (s *Store) Checkpoint(id uint64) (CheckpointManifest, error) {
	inner, err := s.store.Checkpoint(id)
	if err != nil {
		return CheckpointManifest{}, err
	}
	out := CheckpointManifest{
		ID:           inner.ID,
		CreatedAt:    inner.CreatedAt,
		Trajectories: make([]TrajectoryBaseline, len(inner.Trajectories)),
	}
	for i, baseline := range inner.Trajectories {
		out.Trajectories[i] = TrajectoryBaseline{
			Namespace:  baseline.Namespace,
			Session:    baseline.Session,
			ReachedSeq: baseline.ReachedSeq,
			Deadline:   baseline.Deadline,
		}
	}
	return out, nil
}

// Restore resets the store's runtime view — every trajectory's sequence
// counter and the session table — to the values the checkpoint recorded:
// appends afterwards continue from the checkpoint's sequence number, and
// an append whose sequence number is already occupied on disk overwrites
// the stored event — last write wins, the overwritten event's bytes
// becoming dead data that the next merge reclaims. A restored timed
// session keeps the deadline the checkpoint recorded, not one recomputed
// at restore time.
//
// Restore is a baseline to recover to, not a time machine: it resets
// runtime state and touches no data. Sessions created after the
// checkpoint and events appended after it stay on disk and stay visible
// to full reads; only the counters and deadlines the manifest recorded
// roll back. Restoring an unknown checkpoint fails with an error
// wrapping ErrCheckpointNotFound and changes nothing.
func (s *Store) Restore(id uint64) error {
	return s.store.Restore(id)
}

// Batch accumulates appends and a checkpoint for one atomic commit: on
// Commit, everything the batch carries — the whole group of events and
// the baseline — is written as a single durable unit, one append and
// one fdatasync, so it either survives as a whole or is not visible at
// all. There is never a half group of events and never a half
// checkpoint. A Batch is not safe for concurrent use by multiple
// goroutines: it is its author's accumulation buffer. Create a fresh
// batch for each group.
type Batch struct {
	batch *semantic.Batch
}

// NewBatch returns an empty batch bound to this store.
func (s *Store) NewBatch() *Batch {
	return &Batch{batch: s.store.NewBatch()}
}

// Append stages payload as the session's next event. Staged events
// become durable and visible only when the batch commits; until then
// they occupy no sequence number, so an abandoned or failed batch
// leaves no hole in any trajectory. The payload is copied and treated
// as opaque bytes. Staging fails when the handle belongs to another
// store or the session's TTL has elapsed (an error wrapping
// ErrSessionEnded).
func (b *Batch) Append(sess *Session, payload []byte) error {
	return b.batch.Append(sess.session, payload)
}

// Checkpoint stages a checkpoint of the whole store into this batch.
// The manifest is written at Commit, after the batch's own events have
// been given their sequence numbers, so it records them: the group of
// events and its baseline become durable together.
func (b *Batch) Checkpoint() {
	b.batch.Checkpoint()
}

// Commit makes everything the batch holds durable in one atomic write.
// It allocates the staged events' sequence numbers in staging order and
// returns them; when a checkpoint was staged it also returns the
// checkpoint's ID, and 0 otherwise. Sequence numbers are allocated, and
// the trajectories' counters advance, only after the whole batch is
// durable, so a failed commit leaves no hole behind. A commit fails
// whole — with an error wrapping ErrSessionEnded when a staged session's
// TTL has elapsed by commit time, ErrClosed after Close, or an error
// for a payload beyond the storable size — and then nothing the batch
// carried is visible: no event, no checkpoint. Commit clears the batch,
// so it can be reused.
func (b *Batch) Commit() ([]uint64, uint64, error) {
	return b.batch.Commit()
}

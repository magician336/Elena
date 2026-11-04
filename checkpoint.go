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

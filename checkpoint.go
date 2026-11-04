package elena

import "time"

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

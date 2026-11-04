package semantic

import "time"

// Merge triggers one engine merge: every live entry is rewritten into a
// fresh data file while every expired entry is left out — omission is
// the physical reclamation. The rewrite drops everything expired on
// disk, not just the sessions the sweep tracks, so one merge reclaims
// elapsed sessions and any expired residue alike. Visibility does not
// move: expired entries were already invisible on the read path before
// any merge; a merge only returns their bytes and index memory.
//
// Deadlines that have elapsed leave the sweep's deadline view here:
// whatever this merge reclaimed needs no further signal. A deadline
// whose merge fails is reclaimed by the next merge, whenever one runs.
func (s *Store) Merge() error {
	now := time.Now()
	s.mu.Lock()
	for key, deadline := range s.deadlines {
		if !now.Before(deadline) {
			delete(s.deadlines, key)
		}
	}
	s.mu.Unlock()

	if err := s.eng.Merge(); err != nil {
		return opErr("merge", err)
	}
	return nil
}

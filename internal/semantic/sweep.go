package semantic

import (
	"errors"
	"fmt"
	"time"

	"github.com/magician336/elena/internal/engine"
)

// collectDeadlines builds the sweep's initial deadline view: one scan
// of every namespace's live session metadata. The engine reports only
// entries whose deadline has not elapsed, so the view holds exactly the
// pending deadlines of the directory — sessions stored by earlier
// processes included, attached or not. Sessions already expired and not
// yet merged are invisible to the scan by construction; their bytes are
// reclaimed by the next merge this store runs, whenever a signal or a
// manual call triggers one. Runs before the sweep starts, on a store
// not yet shared, so no lock is taken.
func (s *Store) collectDeadlines() error {
	err := s.eng.Scan(engine.ScanOptions{Prefix: []byte{keyAreaSession}}, func(k, value []byte) error {
		namespace, id, kind, ok := parseSessionKey(k)
		if !ok {
			return fmt.Errorf("elena: collect deadlines: %w", errCorruptedSessionKey)
		}
		if kind != keyKindMetadata {
			return nil
		}
		form, deadline, derr := decodeMetadata(value)
		if derr != nil {
			return fmt.Errorf("elena: collect deadlines: %w", derr)
		}
		if form.timed {
			s.deadlines[trajKey{namespace: namespace, session: id}] = deadline
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedSessionKey) || errors.Is(err, errMalformedMetadata) {
			return err
		}
		return opErr("collect deadlines", err)
	}
	return nil
}

// Merge triggers one engine merge: every live entry is rewritten into a
// fresh data file while every expired entry is left out — omission is
// the physical reclamation. The rewrite drops everything expired on
// disk, not just the sessions the sweep tracks, so one merge reclaims
// elapsed sessions and any expired residue alike. Visibility does not
// move: expired entries were already invisible on the read path before
// any merge; a merge only returns their bytes and index memory.
//
// Deadlines that have elapsed leave the sweep's deadline view here:
// whatever this merge reclaims needs no further signal. A deadline
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

// sweepDue reports whether the deadline view holds an elapsed deadline
// — a timed session whose bytes a merge should now reclaim. It only
// looks: consuming the signal is Merge's job, so a check followed by a
// failed merge leaves the deadline pending and the next check retries.
func (s *Store) sweepDue() bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, deadline := range s.deadlines {
		if !now.Before(deadline) {
			return true
		}
	}
	return false
}

// sweepForever is the background sweep. Once per period it checks the
// deadline view; when a deadline has elapsed it triggers one merge —
// the sweep never deletes events or writes tombstones, it only knows
// when a merge is due. It exits when Close closes sweepStop; sweepDone
// is closed on exit, which is what Close joins on.
func (s *Store) sweepForever(period time.Duration) {
	defer close(s.sweepDone)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-s.sweepStop:
			return
		case <-ticker.C:
			if s.sweepDue() {
				s.runSweepMerge()
			}
		}
	}
}

// runSweepMerge performs the merge a due deadline asked for. A failure
// has no caller to return to: the first failure is recorded and Close
// surfaces it, mirroring how the engine reports a failed background
// flush. The deadline stays or leaves the view exactly as Merge's own
// rules dictate; whatever bytes linger are reclaimed by the next merge
// that runs.
func (s *Store) runSweepMerge() {
	err := s.Merge()
	if err != nil && s.sweepErr.Load() == nil {
		s.sweepErr.Store(&err)
	}
}

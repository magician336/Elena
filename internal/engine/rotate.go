package engine

import "fmt"

// Active-file rotation: once the active data file grows beyond the
// configured Options.MaxDataFileSize, it is closed as an immutable
// file (its file ID never receives appends again) and a new active
// file with the next monotonic ID takes over. The immutable file
// stays open for reads because the in-memory index and later Gets
// still reference its entries.

// rotateActive turns the current active file into an immutable file
// and starts a fresh active file under the next file ID. The active
// ID and handle are captured under mu because a concurrent merge can
// swap the engine's file set even while this writer holds writeMu
// (a merge takes writeMu only for its short critical sections). Under
// the interval policy the outgoing file is fdatasync'ed first: once
// rotated away, no active-file flush will ever cover it again, and its
// hint is published durably right after — data and hint must agree on
// disk (always already synced every entry; none forgoes data syncs by
// definition, and recovery tolerates a hint that outruns its data by
// discarding the hint). The hint is written next, so a crash
// mid-rotation can leave the previous file without a hint (Open falls
// back to scanning it) but never leaves a new active file whose
// predecessor lacks one. The file-set update happens under mu so
// concurrent readers never observe a half-rotated engine (lock order
// writeMu before mu is preserved). The caller must hold writeMu.
func (e *Engine) rotateActive() error {
	e.mu.RLock()
	oldID := e.activeID
	df := e.files[oldID]
	e.mu.RUnlock()
	if df == nil {
		return fmt.Errorf("engine: active data file %d not open for rotation", oldID)
	}
	if e.syncPolicy == SyncInterval {
		if err := syncDataFile(df.f); err != nil {
			return err
		}
	}
	if err := e.writeHintFile(oldID, df.f); err != nil {
		return err
	}
	next := oldID + 1
	f, err := e.openNewDataFile(next)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.files[next] = &dataFile{f: f}
	e.activeID = next
	e.activeOffset = 0
	e.mu.Unlock()
	return nil
}

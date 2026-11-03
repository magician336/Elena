package engine

// Active-file rotation: once the active data file grows beyond the
// configured Options.MaxDataFileSize, it is closed as an immutable
// file (its file ID never receives appends again) and a new active
// file with the next monotonic ID takes over. The immutable file
// stays open for reads because the in-memory index and later Gets
// still reference its entries.

// rotateIfDue rotates the active data file once its size has grown
// beyond the configured threshold. It runs after a durable append, so
// the write the caller acknowledged is already safe; a rotation
// failure is reported but never rolls data back — the engine simply
// keeps appending to the current active file until rotation succeeds
// on a later write. The caller must hold writeMu.
func (e *Engine) rotateIfDue() error {
	if e.activeOffset <= int64(e.maxDataFileSize) {
		return nil
	}
	return e.rotateActive()
}

// rotateActive turns the current active file into an immutable file
// and starts a fresh active file under the next file ID. The hint is
// written first, so a crash mid-rotation can leave the previous file
// without a hint (Open falls back to scanning it) but never leaves a
// new active file whose predecessor lacks one. The file-set update
// happens under mu so concurrent readers never observe a half-rotated
// engine (lock order writeMu before mu is preserved). The caller must
// hold writeMu.
func (e *Engine) rotateActive() error {
	if err := e.writeHintFile(e.activeID); err != nil {
		return err
	}
	next := e.activeID + 1
	f, err := e.openNewDataFile(next)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.files[next] = f
	e.activeID = next
	e.activeOffset = 0
	e.mu.Unlock()
	return nil
}

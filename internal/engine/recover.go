package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// recoverFiles loads every data file found in the engine directory.
// Per file, a valid hint file is preferred: its records rebuild the
// index without reading the data file. Whatever the hint does not
// cover — a missing hint, or bytes appended after the hint was
// written — is rebuilt by scanning the data file itself, applying
// entries in order (puts set the key, tombstones clear it) and
// truncating a torn or corrupted tail, the remains of a crash
// mid-append. Hints are accelerators: correctness never depends on
// them, only the scan is authoritative.
func (e *Engine) recoverFiles() error {
	ids, err := listDataFileIDs(e.dir)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := e.loadDataFile(id); err != nil {
			return err
		}
	}
	return nil
}

// loadDataFile opens one existing data file, replays it into the
// index (hint records first, then a scan of whatever they do not
// cover), truncates an incomplete tail if present, and keeps the file
// open for reads.
func (e *Engine) loadDataFile(id uint32) error {
	path := filepath.Join(e.dir, dataFileName(id))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("engine: open data file %q: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("engine: stat data file %q: %w", path, err)
	}

	covered, hintedRecords, hinted := e.loadFromHint(id, info.Size())
	if !hinted {
		covered = 0
	}

	cleanOffset, scanned, err := e.replayFile(f, id, covered)
	if err != nil {
		_ = f.Close()
		return err
	}

	if info.Size() != cleanOffset {
		// The tail holds a torn or corrupted entry (a crash mid-append
		// was observed here). Drop it and make the truncation durable
		// before any new entry can be appended on top of it.
		if err := f.Truncate(cleanOffset); err != nil {
			_ = f.Close()
			return fmt.Errorf("engine: truncate %q to %d: %w", path, cleanOffset, err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("engine: sync truncated %q: %w", path, err)
		}
	}

	e.files[id] = &dataFile{f: f, entries: uint64(hintedRecords + scanned)}
	// Files load in ascending ID order and the last one becomes the
	// active file, so track its clean end as the append position.
	e.activeOffset = cleanOffset
	return nil
}

// replayFile scans f starting at from, validating and applying every
// complete entry to the index, and returns the offset of the first
// byte not covered by a complete valid entry plus the number of
// complete entries it applied. That offset equals the file size when
// the tail is clean.
func (e *Engine) replayFile(f *os.File, id uint32, from int64) (int64, int, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("engine: stat data file: %w", err)
	}
	size := info.Size()

	off := from
	scanned := 0
	header := make([]byte, entryHeaderSize)
	for off < size {
		remaining := size - off
		if remaining < entryHeaderSize {
			return off, scanned, nil // torn header
		}
		if _, err := f.ReadAt(header, off); err != nil {
			return 0, 0, fmt.Errorf("engine: read header at offset %d: %w", off, err)
		}
		h := decodeHeader(header)
		total := int64(entryHeaderSize) + int64(h.keyLen)
		if h.valueLen != tombstoneValueLen {
			total += int64(h.valueLen)
		}
		if total > remaining {
			return off, scanned, nil // impossible lengths or torn body
		}

		entry := make([]byte, total)
		if _, err := f.ReadAt(entry, off); err != nil {
			return 0, 0, fmt.Errorf("engine: read entry at offset %d: %w", off, err)
		}
		if entryCRC(entry) != h.crc {
			return off, scanned, nil // corrupted entry (e.g. partial write)
		}

		key := string(entry[entryHeaderSize : entryHeaderSize+int(h.keyLen)])
		if h.valueLen == tombstoneValueLen {
			delete(e.index, key)
		} else {
			e.index[key] = entryRef{fileID: id, offset: off, size: uint32(total)}
		}
		off += total
		scanned++
	}
	return off, scanned, nil
}

// createActive creates a brand-new data file for appends, syncs the
// directory entry (so the file is guaranteed to survive a crash before
// the first Put ack), and registers it as the engine's open file for
// id. Callers that must register under a lock (rotation, while readers
// are live) use openNewDataFile plus their own registration instead.
func (e *Engine) createActive(id uint32) error {
	f, err := e.openNewDataFile(id)
	if err != nil {
		return err
	}
	e.files[id] = &dataFile{f: f}
	return nil
}

// openNewDataFile creates and durably syncs a brand-new data file for
// id without registering it in the engine's file set.
func (e *Engine) openNewDataFile(id uint32) (*os.File, error) {
	path := filepath.Join(e.dir, dataFileName(id))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("engine: create data file %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("engine: sync new data file %q: %w", path, err)
	}
	if err := syncDir(e.dir); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

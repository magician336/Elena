package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// recoverFiles loads every data file found in the engine directory:
// each file is scanned from the start, its entries are applied to the
// index in order (puts set the key, tombstones clear it), and a torn
// or corrupted tail — the remains of a crash mid-append — is
// truncated so the file ends on its last complete entry.
//
// This is the only index-building path: Open always goes through it.
// A later ticket adds hint files as an acceleration; correctness never
// depends on them.
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
// index, truncates an incomplete tail if present, and keeps the file
// open for reads.
func (e *Engine) loadDataFile(id uint32) error {
	path := filepath.Join(e.dir, dataFileName(id))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("engine: open data file %q: %w", path, err)
	}

	cleanOffset, err := e.replayFile(f, id)
	if err != nil {
		_ = f.Close()
		return err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("engine: stat data file %q: %w", path, err)
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

	e.files[id] = f
	// Files load in ascending ID order and the last one becomes the
	// active file, so track its clean end as the append position.
	e.activeOffset = cleanOffset
	return nil
}

// replayFile scans f from the start, validating and applying every
// complete entry to the index, and returns the offset of the first
// byte not covered by a complete valid entry. That offset equals the
// file size when the tail is clean.
func (e *Engine) replayFile(f *os.File, id uint32) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("engine: stat data file: %w", err)
	}
	size := info.Size()

	var off int64
	header := make([]byte, entryHeaderSize)
	for off < size {
		remaining := size - off
		if remaining < entryHeaderSize {
			return off, nil // torn header
		}
		if _, err := f.ReadAt(header, off); err != nil {
			return 0, fmt.Errorf("engine: read header at offset %d: %w", off, err)
		}
		h := decodeHeader(header)
		total := int64(entryHeaderSize) + int64(h.keyLen)
		if h.valueLen != tombstoneValueLen {
			total += int64(h.valueLen)
		}
		if total > remaining {
			return off, nil // impossible lengths or torn body
		}

		entry := make([]byte, total)
		if _, err := f.ReadAt(entry, off); err != nil {
			return 0, fmt.Errorf("engine: read entry at offset %d: %w", off, err)
		}
		if entryCRC(entry) != h.crc {
			return off, nil // corrupted entry (e.g. partial write)
		}

		key := string(entry[entryHeaderSize : entryHeaderSize+int(h.keyLen)])
		if h.valueLen == tombstoneValueLen {
			delete(e.index, key)
		} else {
			e.index[key] = entryRef{fileID: id, offset: off, size: uint32(total)}
		}
		off += total
	}
	return off, nil
}

// createActive creates a brand-new data file for appends and syncs the
// directory entry, so the file is guaranteed to survive a crash before
// the first Put ack.
func (e *Engine) createActive(id uint32) error {
	path := filepath.Join(e.dir, dataFileName(id))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("engine: create data file %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("engine: sync new data file %q: %w", path, err)
	}
	if err := syncDir(e.dir); err != nil {
		_ = f.Close()
		return err
	}
	e.files[id] = f
	return nil
}

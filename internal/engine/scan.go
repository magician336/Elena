package engine

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// ScanOptions bounds what Scan visits. The zero value scans every live
// key in byte-lexicographic order.
type ScanOptions struct {
	// Prefix restricts the scan to keys that start with these bytes.
	// nil or empty matches every key.
	Prefix []byte

	// End is an upper bound on the visited keys themselves (not
	// relative to Prefix). nil or empty means no upper bound;
	// otherwise a key is visited when it is less than End, or equal to
	// End and EndInclusive is set.
	End []byte

	// EndInclusive decides whether a key exactly equal to End is
	// visited (true) or excluded (false).
	EndInclusive bool
}

// scanItem is one entry of a scan's snapshot: the key, its location,
// and the open data file to read it from.
type scanItem struct {
	key string
	ref entryRef
	f   *dataFile
}

// Scan visits every live entry matching opts, in byte-lexicographic
// key order, calling visit with a copy of each key and value. It is
// the engine's iterator primitive for prefix iteration.
//
// Semantics:
//
//   - Live means neither tombstoned nor expired: deleted keys are
//     absent from the index, and entries whose stored expiry timestamp
//     has passed are skipped, so visitors never see either.
//   - The item set is a snapshot taken when Scan starts: Puts and
//     Deletes issued while Scan is running (from other goroutines or
//     from inside visit) do not add or remove items for this scan.
//     Each item's own liveness is still checked at read time.
//   - visit returning a non-nil error stops the scan and the error is
//     returned to the caller as-is.
//   - A closed engine fails with ErrClosed.
func (e *Engine) Scan(opts ScanOptions, visit func(key, value []byte) error) error {
	if visit == nil {
		return errors.New("engine: Scan requires a visit function")
	}
	items, err := e.snapshotItems(opts)
	if err != nil {
		return err
	}

	for _, it := range items {
		value, err := readLiveEntry(it.f.f, it.ref, it.key)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // entry expired between snapshot and read
			}
			return err
		}
		if err := visit([]byte(it.key), value); err != nil {
			return err
		}
	}
	return nil
}

// snapshotItems copies the matching index entries — key, location, and
// data file handle — under the read lock, then returns them sorted by
// key. The lock is held only for the copy: appends never rewrite the
// bytes an existing entry occupies and rotated files stay open, so the
// captured items stay readable after the lock is released even while
// writers keep running.
func (e *Engine) snapshotItems(opts ScanOptions) ([]scanItem, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.closed {
		return nil, ErrClosed
	}
	prefix := string(opts.Prefix)
	end := string(opts.End)
	items := make([]scanItem, 0, len(e.index))
	for key, ref := range e.index {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if end != "" {
			if cmp := strings.Compare(key, end); cmp > 0 || (cmp == 0 && !opts.EndInclusive) {
				continue
			}
		}
		f, ok := e.files[ref.fileID]
		if !ok {
			return nil, fmt.Errorf("engine: data file %d for key %q: %w", ref.fileID, key, ErrCorrupted)
		}
		items = append(items, scanItem{key: key, ref: ref, f: f})
	}
	slices.SortFunc(items, func(a, b scanItem) int { return strings.Compare(a.key, b.key) })
	return items, nil
}

// readLiveEntry reads the entry located by ref from f, validates it,
// and returns a copy of its value. An entry whose stored expiry
// timestamp has passed reports ErrNotFound: dead entries look exactly
// like missing ones to every reader.
func readLiveEntry(f *os.File, ref entryRef, key string) ([]byte, error) {
	buf := make([]byte, ref.size)
	if _, err := f.ReadAt(buf, ref.offset); err != nil {
		return nil, fmt.Errorf("engine: read entry at offset %d: %w", ref.offset, err)
	}

	h := decodeHeader(buf)
	start, end := valueBounds(h)
	if entryHeaderSize+int(h.keyLen) > len(buf) || end > len(buf) || start > end {
		return nil, fmt.Errorf("engine: entry at offset %d: %w", ref.offset, ErrCorrupted)
	}
	if entryCRC(buf) != h.crc {
		return nil, fmt.Errorf("engine: entry at offset %d: %w", ref.offset, ErrCorrupted)
	}
	if h.expiredAt(time.Now().UnixNano()) {
		return nil, ErrNotFound
	}

	value := make([]byte, end-start)
	copy(value, buf[start:end])
	return value, nil
}

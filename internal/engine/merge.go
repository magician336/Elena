package engine

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Merge rewrites every live entry into one fresh data file (with its
// hint file), atomically switches the index over, and deletes the old
// files. Dead data — overwritten values, tombstones, and entries whose
// expiry has passed — is not copied and is thereby physically
// reclaimed; unexpired TTL entries are copied byte-for-byte, expiry
// header included.
//
// Concurrency: the merge runs in phases so writers keep appending and
// readers keep serving for its whole duration. A short section under
// writeMu freezes the merge set (every current file — the active one
// included, by rotating it away) and opens two new files: the merge
// output and the successor active file. The rewrite itself holds no
// engine lock. One short section under writeMu+mu then swaps the index
// to the output file (writeMu because a racing append captured its
// offset and handle under mu alone; mu so readers observe either the
// old or the new location of every key, never a mix). Writers that
// raced ahead into the successor active file are untouched by the
// swap. Merge calls are serialized per engine by mergeMu; lock order
// is mergeMu before writeMu before mu.
//
// Crash safety: the output data file and its hint are durably on disk
// before the switch, and both the output and the old files carry a
// higher/lower file ID ordering the replay resolves — a crash at any
// point leaves a directory that reopens to the same live set (the
// leftover files are simply reclaimed by the next merge).
//
// The old files' handles are retired rather than closed on the spot:
// a Scan that snapshotted them moments earlier may still be reading
// through them. Each handle closes when its last reader is done, or at
// Close at the latest; see dataFile.
func (e *Engine) Merge() error {
	e.mergeMu.Lock()
	defer e.mergeMu.Unlock()
	return e.merge()
}

// merge runs one merge; the caller holds mergeMu.
func (e *Engine) merge() error {
	// ---- Phase 1: freeze the merge set, open output + successor
	// active file. Short, under writeMu so rotation cannot race the
	// file IDs. ----
	e.writeMu.Lock()
	e.mu.RLock()
	closed := e.closed
	var totalEntries uint64
	for _, df := range e.files {
		totalEntries += df.entries
	}
	e.mu.RUnlock()
	if closed {
		e.writeMu.Unlock()
		return ErrClosed
	}
	if totalEntries == 0 {
		e.writeMu.Unlock()
		return nil // nothing on disk: a merge would only churn files
	}

	outID := e.activeID + 1
	nextActiveID := e.activeID + 2
	out, err := e.openNewDataFile(outID)
	if err != nil {
		e.writeMu.Unlock()
		return err
	}
	nextActive, err := e.openNewDataFile(nextActiveID)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(filepath.Join(e.dir, dataFileName(outID)))
		_ = syncDir(e.dir)
		e.writeMu.Unlock()
		return err
	}

	merged := make([]uint32, 0, len(e.files))
	e.mu.Lock()
	for id := range e.files {
		merged = append(merged, id) // includes the active file: rotating it away makes the whole disk immutable
	}
	e.files[outID] = &dataFile{f: out}
	e.files[nextActiveID] = &dataFile{f: nextActive}
	e.activeID = nextActiveID
	e.activeOffset = 0
	e.mu.Unlock()
	e.writeMu.Unlock()

	// Every file in merged is immutable from here on: appends race
	// only with nextActiveID, which no snapshot references.

	newRefs, flushed, err := e.rewriteInto(out, outID, merged)
	if err != nil {
		e.discardMergeFiles(outID)
		return err
	}

	// ---- Phase 3: atomic index switch. Short, under writeMu+mu: the
	// writeMu grab keeps a concurrent append (which captured the old
	// active handle and offset under mu alone) from racing the swap,
	// and the mu grab keeps readers from observing a half-switched
	// index. ----
	mergedSet := make(map[uint32]struct{}, len(merged))
	for _, id := range merged {
		mergedSet[id] = struct{}{}
	}
	e.writeMu.Lock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		e.writeMu.Unlock()
		e.discardMergeFiles(outID, nextActiveID)
		return ErrClosed
	}
	for key, ref := range e.index {
		if _, old := mergedSet[ref.fileID]; !old {
			continue // writer raced ahead into the successor active file
		}
		e.liveBytes -= uint64(ref.size)
		if newRef, ok := newRefs[key]; ok {
			e.index[key] = newRef
			e.liveBytes += uint64(newRef.size)
		} else {
			delete(e.index, key) // the entry expired before or during the rewrite
		}
	}
	for _, id := range merged {
		df := e.files[id]
		delete(e.files, id)
		df.retired.Store(true)
		if df.scanRefs.Load() == 0 {
			// No scan is reading through it; the handle closes now.
			_ = df.close()
		} else {
			// An in-flight scan still reads through the handle; the
			// last reader closes it (Close is the backstop).
			e.retired = append(e.retired, df)
		}
	}
	e.files[outID].entries = uint64(len(newRefs))
	e.files[outID].bytes = int64(flushed)
	e.mu.Unlock()
	e.writeMu.Unlock()

	// ---- Phase 4: the old files are unreferenced; drop them. ----
	var firstErr error
	for _, id := range merged {
		if err := os.Remove(filepath.Join(e.dir, dataFileName(id))); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("engine: merge: remove data file %d: %w", id, err)
		}
		_ = os.Remove(filepath.Join(e.dir, hintFileName(id)))
		_ = os.Remove(filepath.Join(e.dir, hintFileName(id)+hintTmpSuffix))
	}
	if err := syncDir(e.dir); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		return firstErr // files left behind are reclaimed by the next merge
	}
	e.opsMerge.Add(1)
	return nil
}

// rewriteItem is one snapshot entry of the merge: a key, its location
// in a now-immutable file, and that file's handle.
type rewriteItem struct {
	key string
	ref entryRef
	df  *dataFile
}

// rewriteInto copies every live entry referenced by the index from the
// merged files into out, streams the matching hint file next to it,
// makes both durable, and returns the key locations inside out. The
// rewrite holds no engine lock: the merged files are immutable, and
// the index entries they contribute cannot move (writers only ever
// publish locations in the successor active file). Entries that fail
// their checksum abort the merge; entries whose expiry has passed are
// dropped — that is the physical reclamation.
func (e *Engine) rewriteInto(out *os.File, outID uint32, merged []uint32) (map[string]entryRef, uint64, error) {
	mergedSet := make(map[uint32]struct{}, len(merged))
	for _, id := range merged {
		mergedSet[id] = struct{}{}
	}

	e.mu.RLock()
	items := make([]rewriteItem, 0, len(e.index))
	for key, ref := range e.index {
		if _, old := mergedSet[ref.fileID]; !old {
			continue
		}
		items = append(items, rewriteItem{key: key, ref: ref, df: e.files[ref.fileID]})
	}
	e.mu.RUnlock()

	hintPath := filepath.Join(e.dir, hintFileName(outID))
	hintTmpPath := hintPath + hintTmpSuffix
	hw, err := os.OpenFile(hintTmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("engine: merge: create hint tmp %q: %w", hintTmpPath, err)
	}
	dataW := bufio.NewWriterSize(out, 64<<10)
	hintW := bufio.NewWriterSize(hw, 64<<10)

	newRefs := make(map[string]entryRef, len(items))
	now := time.Now().UnixNano()
	var off int64
	var flushed uint64
	buf := make([]byte, 4096)
	for _, it := range items {
		if uint32(cap(buf)) < it.ref.size {
			buf = make([]byte, it.ref.size)
		}
		buf = buf[:it.ref.size]
		if _, err := it.df.f.ReadAt(buf, it.ref.offset); err != nil {
			return nil, 0, fmt.Errorf("engine: merge: read entry for key %q at offset %d: %w", it.key, it.ref.offset, err)
		}
		h := decodeHeader(buf)
		if entryCRC(buf) != h.crc {
			return nil, 0, fmt.Errorf("engine: merge: entry for key %q at offset %d: %w", it.key, it.ref.offset, ErrCorrupted)
		}
		if h.expiredAt(now) {
			continue // dead: physically reclaimed by omission
		}
		if _, err := dataW.Write(buf); err != nil {
			return nil, 0, fmt.Errorf("engine: merge: write entry for key %q: %w", it.key, err)
		}
		key := buf[entryHeaderSize : entryHeaderSize+int(h.keyLen)]
		if err := writeHintRecord(hintW, off, key, h.valueLen); err != nil {
			return nil, 0, fmt.Errorf("engine: merge: write hint record for key %q: %w", it.key, err)
		}
		newRefs[it.key] = entryRef{fileID: outID, offset: off, size: it.ref.size}
		off += int64(it.ref.size)
		flushed += uint64(it.ref.size)
	}

	if err := dataW.Flush(); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: flush data writer: %w", err)
	}
	if err := out.Sync(); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: sync output data file: %w", err)
	}
	if err := hintW.Flush(); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: flush hint writer: %w", err)
	}
	if err := hw.Sync(); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: sync hint tmp: %w", err)
	}
	if err := hw.Close(); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: close hint tmp: %w", err)
	}
	if err := os.Rename(hintTmpPath, hintPath); err != nil {
		return nil, 0, fmt.Errorf("engine: merge: publish hint %q: %w", hintPath, err)
	}
	e.bytesFlushed.Add(flushed) // the rewrite is disk traffic: write amplification includes merges
	return newRefs, flushed, nil
}

// discardMergeFiles removes the files named for ids from disk and, if
// the engine still holds their handles, closes them. Failure paths
// only: correctness never depends on this — a later Open replays and
// truncates leftovers, and a later merge reclaims them.
func (e *Engine) discardMergeFiles(ids ...uint32) {
	e.mu.Lock()
	for _, id := range ids {
		if df := e.files[id]; df != nil {
			_ = df.close()
			delete(e.files, id)
		}
	}
	e.mu.Unlock()
	for _, id := range ids {
		_ = os.Remove(filepath.Join(e.dir, dataFileName(id)))
		_ = os.Remove(filepath.Join(e.dir, hintFileName(id)))
		_ = os.Remove(filepath.Join(e.dir, hintFileName(id)+hintTmpSuffix))
	}
	_ = syncDir(e.dir)
}

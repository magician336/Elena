package engine

import (
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

// ErrNotFound is returned by Get when the key was never written or has
// been deleted: from the reader's point of view both states are
// "does not exist".
var ErrNotFound = errors.New("engine: key not found")

// ErrClosed is returned by engine operations attempted after Close.
var ErrClosed = errors.New("engine: engine is closed")

// ErrInvalidTTL is returned by PutWithTTL when the ttl is zero or
// negative (an entry that is born expired carries no information) or so
// large that the expiry timestamp is not representable in the entry
// header.
var ErrInvalidTTL = errors.New("engine: invalid ttl")

// ErrKeyTooLarge is returned by Put when the key exceeds the
// configured MaxKeySize.
var ErrKeyTooLarge = errors.New("engine: key too large")

// ErrValueTooLarge is returned by Put when the value exceeds the
// configured MaxValueSize.
var ErrValueTooLarge = errors.New("engine: value too large")

// ErrCorrupted is returned when a stored entry fails its checksum or
// structural validation at read time.
var ErrCorrupted = errors.New("engine: stored entry corrupted")

// entryRef locates one entry inside a data file: the file ID, the byte
// offset of the entry start, and its total encoded size.
type entryRef struct {
	fileID uint32
	offset int64
	size   uint32
}

// Put stores value under key with no expiry. The entry is appended to
// the active data file and made durable per the engine's sync policy
// (under the default always policy a successful Put is durable the
// moment it returns; see SyncPolicy for the other tiers), then the
// in-memory index is updated so the new entry wins over any older one.
//
// One error asymmetry is deliberate: when the append pushes the active
// file past Options.MaxDataFileSize and the following rotation fails
// (for instance its hint write hits a checksum-failing entry), Put
// returns that error even though this write itself is already durable
// and visible — a "failed" Put may nevertheless be present after
// reopen, never the other way around. Delete, PutWithTTL, and Batch
// Commit share this write path and the same asymmetry.
func (e *Engine) Put(key, value []byte) error {
	e.opsPut.Add(1)
	if err := e.putExpires(key, value, 0); err != nil {
		return err
	}
	e.maybeAutoMerge()
	return nil
}

// PutWithTTL stores value under key with a time-to-live: once ttl has
// elapsed since the call, the entry becomes invisible to Get and Scan —
// exactly as if it had never been written. Visibility is decided
// against the expiry timestamp stored in the entry header on disk, so
// an expired entry stays invisible across Close and Open. Physical
// reclamation of expired entries is the merge's job, not the read
// path's.
func (e *Engine) PutWithTTL(key, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("engine: ttl %d: %w", ttl, ErrInvalidTTL)
	}
	deadline := time.Now().Add(ttl)
	if deadline.Before(maxExpiryDeadline) {
		e.opsPutWithTTL.Add(1)
		if err := e.putExpires(key, value, deadline.UnixNano()); err != nil {
			return err
		}
		e.maybeAutoMerge()
		return nil
	}
	return fmt.Errorf("engine: ttl %d: %w", ttl, ErrInvalidTTL)
}

// maxExpiryDeadline is the latest expiry instant the entry header can
// represent: expires is an int64 of Unix nanoseconds, which runs out in
// year 2262. A deadline at or beyond it cannot be stored honestly, so
// such TTLs are rejected instead of silently wrapping.
var maxExpiryDeadline = time.Unix(0, math.MaxInt64)

// putExpires is the shared write path of Put and PutWithTTL: it appends
// one value entry whose header carries expires (0 = never expires),
// makes it durable per the engine's sync policy, and publishes it in
// the index.
func (e *Engine) putExpires(key, value []byte, expires int64) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	offset := e.activeOffset
	active := e.files[e.activeID]
	maxKey, maxVal, maxSize := e.maxKeySize, e.maxValueSize, e.maxDataFileSize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if active == nil {
		return fmt.Errorf("engine: active data file %d not open for append", e.activeID)
	}
	if len(key) > maxKey {
		return fmt.Errorf("engine: put key of %d bytes: %w (max %d)", len(key), ErrKeyTooLarge, maxKey)
	}
	if len(value) > maxVal {
		return fmt.Errorf("engine: put value of %d bytes: %w (max %d)", len(value), ErrValueTooLarge, maxVal)
	}

	buf := appendValueEntry(nil, time.Now().UnixNano(), expires, key, value)
	if err := e.appendActive(active.f, buf); err != nil {
		return err
	}
	e.bytesWritten.Add(uint64(len(key) + len(value)))

	e.mu.Lock()
	if old, ok := e.index[string(key)]; ok {
		e.liveBytes -= uint64(old.size)
	}
	e.index[string(key)] = entryRef{fileID: e.activeID, offset: offset, size: uint32(len(buf))}
	e.liveBytes += uint64(len(buf))
	active.entries++
	active.bytes += int64(len(buf))
	e.activeOffset += int64(len(buf))
	rotate := e.activeOffset > int64(maxSize)
	e.mu.Unlock()
	if rotate {
		return e.rotateActive()
	}
	return nil
}

// Get returns the value stored under key, or ErrNotFound when the key
// does not exist. The value is located through the in-memory index and
// read from the data file; the stored checksum is verified on the way
// out.
func (e *Engine) Get(key []byte) ([]byte, error) {
	e.opsGet.Add(1)
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.closed {
		return nil, ErrClosed
	}
	ref, ok := e.index[string(key)]
	if !ok {
		return nil, ErrNotFound
	}
	return e.readEntry(ref, key)
}

// Delete removes key from the engine's point of view by appending a
// tombstone entry (durability follows the engine's sync policy, same
// as Put) and dropping the key from the index. The tombstone stays on
// disk until a later merge reclaims it. Deleting a missing key is not
// an error.
func (e *Engine) Delete(key []byte) error {
	e.opsDelete.Add(1)
	if err := e.deleteKey(key); err != nil {
		return err
	}
	e.maybeAutoMerge()
	return nil
}

// deleteKey appends the tombstone and drops the key from the index;
// the caller must not hold any engine lock.
func (e *Engine) deleteKey(key []byte) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	active := e.files[e.activeID]
	maxKey, maxSize := e.maxKeySize, e.maxDataFileSize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if active == nil {
		return fmt.Errorf("engine: active data file %d not open for append", e.activeID)
	}
	if len(key) > maxKey {
		return fmt.Errorf("engine: delete key of %d bytes: %w (max %d)", len(key), ErrKeyTooLarge, maxKey)
	}

	buf := appendTombstoneEntry(nil, time.Now().UnixNano(), key)
	if err := e.appendActive(active.f, buf); err != nil {
		return err
	}

	e.mu.Lock()
	if old, ok := e.index[string(key)]; ok {
		e.liveBytes -= uint64(old.size)
		delete(e.index, string(key))
	}
	active.entries++
	active.bytes += int64(len(buf))
	e.activeOffset += int64(len(buf))
	rotate := e.activeOffset > int64(maxSize)
	e.mu.Unlock()
	if rotate {
		return e.rotateActive()
	}
	return nil
}

// maybeAutoMerge merges when the configured dead-ratio threshold is
// exceeded. It runs on the write path after the write is durable, so
// the cost lands on the write that crossed the threshold while every
// other writer keeps appending (Merge never blocks writers for its
// rewrite phase). A failed auto merge is dropped here: the triggering
// write already succeeded, and the next threshold-crossing write tries
// again. Must not be called with any engine lock held.
func (e *Engine) maybeAutoMerge() {
	if e.mergeDeadRatio <= 0 {
		return
	}
	e.mu.RLock()
	var total int64
	for _, df := range e.files {
		total += df.bytes
	}
	live := e.liveBytes
	e.mu.RUnlock()
	if total <= 0 || live > uint64(total) {
		return
	}
	if float64(total-int64(live))/float64(total) <= e.mergeDeadRatio {
		return
	}
	_ = e.Merge()
}

// appendActive writes buf to the active file and makes it durable
// according to the engine's sync policy: always fdatasyncs before
// returning (an acknowledged write is on disk), interval leaves the
// sync to the background flusher (Close and rotation force one), none
// never syncs (the bytes ride the OS page cache). The caller must hold
// writeMu and must have captured the handle under mu in the same
// critical section that produced the append offset (a merge swaps the
// active file only under writeMu, so the pair stays coherent).
func (e *Engine) appendActive(active *os.File, buf []byte) error {
	n, err := active.Write(buf)
	if err != nil {
		return fmt.Errorf("engine: append to data file: %w", err)
	}
	if n != len(buf) {
		return fmt.Errorf("engine: short append to data file: wrote %d of %d bytes", n, len(buf))
	}
	if e.syncPolicy == SyncAlways {
		if err := syncDataFile(active); err != nil {
			return err
		}
	}
	e.bytesFlushed.Add(uint64(len(buf)))
	return nil
}

// readEntry reads the entry located by ref and returns a copy of its
// value, or ErrNotFound when the entry's on-disk expiry timestamp says
// it is dead. The expiry verdict always comes from the entry header on
// disk, never from in-memory state, so it survives Close/Open cycles.
// The caller must hold mu (read or write) so the index entry stays
// valid and the active file is not mutated mid-read.
func (e *Engine) readEntry(ref entryRef, key []byte) ([]byte, error) {
	df, ok := e.files[ref.fileID]
	if !ok {
		return nil, fmt.Errorf("engine: data file %d for key %q: %w", ref.fileID, key, ErrCorrupted)
	}
	return readLiveEntry(df.f, ref, string(key))
}

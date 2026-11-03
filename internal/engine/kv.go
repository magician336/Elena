package engine

import (
	"errors"
	"fmt"
	"math"
	"syscall"
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
// the active data file and fdatasync'ed to disk before Put returns
// (the always sync policy: a successful Put is durable), then the
// in-memory index is updated so the new entry wins over any older one.
func (e *Engine) Put(key, value []byte) error {
	e.opsPut.Add(1)
	return e.putExpires(key, value, 0)
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
		return e.putExpires(key, value, deadline.UnixNano())
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
// makes it durable, and publishes it in the index.
func (e *Engine) putExpires(key, value []byte, expires int64) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	offset := e.activeOffset
	maxKey, maxVal := e.maxKeySize, e.maxValueSize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if len(key) > maxKey {
		return fmt.Errorf("engine: put key of %d bytes: %w (max %d)", len(key), ErrKeyTooLarge, maxKey)
	}
	if len(value) > maxVal {
		return fmt.Errorf("engine: put value of %d bytes: %w (max %d)", len(value), ErrValueTooLarge, maxVal)
	}

	buf := appendValueEntry(nil, time.Now().UnixNano(), expires, key, value)
	if err := e.appendDurable(buf); err != nil {
		return err
	}
	e.bytesWritten.Add(uint64(len(key) + len(value)))

	e.mu.Lock()
	e.index[string(key)] = entryRef{fileID: e.activeID, offset: offset, size: uint32(len(buf))}
	e.activeOffset += int64(len(buf))
	e.mu.Unlock()
	return e.rotateIfDue()
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
// tombstone entry (durable, same always sync policy as Put) and
// dropping the key from the index. The tombstone stays on disk until
// a later merge reclaims it. Deleting a missing key is not an error.
func (e *Engine) Delete(key []byte) error {
	e.opsDelete.Add(1)
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	maxKey := e.maxKeySize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if len(key) > maxKey {
		return fmt.Errorf("engine: delete key of %d bytes: %w (max %d)", len(key), ErrKeyTooLarge, maxKey)
	}

	buf := appendTombstoneEntry(nil, time.Now().UnixNano(), key)
	if err := e.appendDurable(buf); err != nil {
		return err
	}

	e.mu.Lock()
	delete(e.index, string(key))
	e.activeOffset += int64(len(buf))
	e.mu.Unlock()
	return e.rotateIfDue()
}

// appendDurable writes buf to the active file and fdatasyncs it before
// returning, so the bytes are on disk by the time the caller's Put is
// acknowledged. The caller must hold writeMu.
func (e *Engine) appendDurable(buf []byte) error {
	active := e.files[e.activeID]
	n, err := active.Write(buf)
	if err != nil {
		return fmt.Errorf("engine: append to data file: %w", err)
	}
	if n != len(buf) {
		return fmt.Errorf("engine: short append to data file: wrote %d of %d bytes", n, len(buf))
	}
	if err := syscall.Fdatasync(int(active.Fd())); err != nil {
		return fmt.Errorf("engine: fdatasync data file: %w", err)
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
	f, ok := e.files[ref.fileID]
	if !ok {
		return nil, fmt.Errorf("engine: data file %d for key %q: %w", ref.fileID, key, ErrCorrupted)
	}
	return readLiveEntry(f, ref, string(key))
}

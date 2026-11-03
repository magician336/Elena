package engine

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// ErrNotFound is returned by Get when the key was never written or has
// been deleted: from the reader's point of view both states are
// "does not exist".
var ErrNotFound = errors.New("engine: key not found")

// ErrClosed is returned by engine operations attempted after Close.
var ErrClosed = errors.New("engine: engine is closed")

// ErrCorrupted is returned when a stored entry fails its checksum or
// structural validation at read time.
var ErrCorrupted = errors.New("engine: stored entry corrupted")

// entryRef locates one entry inside a data file: the byte offset of
// the entry start and its total encoded size.
type entryRef struct {
	offset int64
	size   uint32
}

// Put stores value under key. The entry is appended to the active
// data file and fdatasync'ed to disk before Put returns (the always
// sync policy: a successful Put is durable), then the in-memory index
// is updated so the new entry wins over any older one.
func (e *Engine) Put(key, value []byte) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	offset := e.activeOffset
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}

	buf := appendValueEntry(nil, time.Now().UnixNano(), 0, key, value)
	if err := e.appendDurable(buf); err != nil {
		return err
	}

	e.mu.Lock()
	e.index[string(key)] = entryRef{offset: offset, size: uint32(len(buf))}
	e.activeOffset += int64(len(buf))
	e.mu.Unlock()
	return nil
}

// Get returns the value stored under key, or ErrNotFound when the key
// does not exist. The value is located through the in-memory index and
// read from the data file; the stored checksum is verified on the way
// out.
func (e *Engine) Get(key []byte) ([]byte, error) {
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

// appendDurable writes buf to the active file and fdatasyncs it before
// returning, so the bytes are on disk by the time the caller's Put is
// acknowledged. The caller must hold writeMu.
func (e *Engine) appendDurable(buf []byte) error {
	n, err := e.active.Write(buf)
	if err != nil {
		return fmt.Errorf("engine: append to data file: %w", err)
	}
	if n != len(buf) {
		return fmt.Errorf("engine: short append to data file: wrote %d of %d bytes", n, len(buf))
	}
	if err := syscall.Fdatasync(int(e.active.Fd())); err != nil {
		return fmt.Errorf("engine: fdatasync data file: %w", err)
	}
	return nil
}

// readEntry reads the entry located by ref and returns a copy of its
// value. The caller must hold mu (read or write) so the active file is
// not mutated mid-read and the index entry stays valid.
func (e *Engine) readEntry(ref entryRef, key []byte) ([]byte, error) {
	buf := make([]byte, ref.size)
	if _, err := e.active.ReadAt(buf, ref.offset); err != nil {
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

	value := make([]byte, end-start)
	copy(value, buf[start:end])
	return value, nil
}

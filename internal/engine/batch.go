package engine

import (
	"fmt"
	"time"
)

// Batch accumulates writes for one atomic Commit.
//
// A Commit is durable atomicity, not a transaction:
//
//   - What it guarantees: one Commit means one append buffer, one
//     fdatasync, and one index publication. After Commit returns, the
//     whole batch is durable and visible; before Commit returns, no
//     part of the batch is visible to any reader (including the batch
//     author). A crash can therefore never expose half a batch — the
//     surviving bytes are always a whole number of committed batches,
//     and a torn tail is truncated at the next Open.
//   - What it does not guarantee: isolation and rollback. There are no
//     isolation levels and no snapshot reads; other writers interleave
//     with the batch author only through the engine's single-writer
//     serialization (same-writer ordering: calls take effect in the
//     order the single writer issued them). And once a Commit has been
//     made durable there is no way to undo it — the engine's log is
//     append-only, so "roll back the batch" simply does not exist. A
//     failed Commit leaves no index trace, but the durable state of the
//     engine after an I/O error is undefined until a fresh Open.
//
// A Batch belongs to the Engine that created it and is not safe for
// concurrent use by multiple goroutines: it is the author's
// accumulation buffer.
type Batch struct {
	eng *Engine
	ops []batchOp
}

// batchOp is one accumulated write. key is an immutable copy;
// value is an immutable copy, nil for tombstones. expires is the
// Unix-nanosecond deadline captured when the op was added (0 = never).
type batchOp struct {
	key       string
	value     []byte
	expires   int64
	tombstone bool
}

// NewBatch returns an empty Batch bound to this Engine.
func (e *Engine) NewBatch() *Batch {
	return &Batch{eng: e}
}

// Put stages a value entry in the batch. The write becomes visible only
// when the batch is committed. The key and value are copied, so the
// caller may reuse their slices afterwards.
func (b *Batch) Put(key, value []byte) error {
	return b.stage(key, value, 0, false)
}

// PutWithTTL stages a value entry with a time-to-live, measured from
// this call exactly like a single PutWithTTL: the deadline is captured
// now, travels in the entry header inside the batch's durable append,
// and governs visibility after Commit. Key and value are copied.
func (b *Batch) PutWithTTL(key, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("engine: ttl %d: %w", ttl, ErrInvalidTTL)
	}
	deadline := time.Now().Add(ttl)
	if !deadline.Before(maxExpiryDeadline) {
		return fmt.Errorf("engine: ttl %d: %w", ttl, ErrInvalidTTL)
	}
	return b.stage(key, value, deadline.UnixNano(), false)
}

// Delete stages a tombstone for key: at Commit the key disappears from
// readers' point of view, and the tombstone entry rides in the batch's
// single durable append. Deleting a key that does not exist is not an
// error. The key is copied.
func (b *Batch) Delete(key []byte) error {
	return b.stage(key, nil, 0, true)
}

// stage validates and records one op. Validation happens at staging
// time so a rejected op leaves no trace and the batch stays clean.
func (b *Batch) stage(key, value []byte, expires int64, tombstone bool) error {
	e := b.eng
	e.mu.RLock()
	closed := e.closed
	maxKey, maxVal := e.maxKeySize, e.maxValueSize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if len(key) > maxKey {
		return fmt.Errorf("engine: batch key of %d bytes: %w (max %d)", len(key), ErrKeyTooLarge, maxKey)
	}
	if !tombstone && len(value) > maxVal {
		return fmt.Errorf("engine: batch value of %d bytes: %w (max %d)", len(value), ErrValueTooLarge, maxVal)
	}
	if tombstone {
		b.ops = append(b.ops, batchOp{key: string(key), tombstone: true})
		return nil
	}
	b.ops = append(b.ops, batchOp{
		key:     string(key),
		value:   append([]byte(nil), value...),
		expires: expires,
	})
	return nil
}

// Commit turns the staged writes into one append buffer, makes that
// buffer durable with a single fdatasync, and publishes every staged
// key in the index in one section — the whole batch becomes visible at
// once. Commit clears the batch, so the Batch can be reused for the
// next one. Committing an empty batch is a no-op.
func (b *Batch) Commit() error {
	e := b.eng
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	offset := e.activeOffset
	active := e.files[e.activeID]
	maxSize := e.maxDataFileSize
	e.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if active == nil {
		return fmt.Errorf("engine: active data file %d not open for append", e.activeID)
	}
	if len(b.ops) == 0 {
		return nil
	}

	// One buffer for the whole batch: a single Write lands every
	// staged entry contiguously in the active file, and a single
	// fdatasync (inside appendDurable) covers them all.
	now := time.Now().UnixNano()
	capacity := 0
	for i := range b.ops {
		op := &b.ops[i]
		capacity += entryHeaderSize + len(op.key) + len(op.value)
	}
	buf := make([]byte, 0, capacity)
	sizes := make([]uint32, 0, len(b.ops))
	var logical uint64
	for i := range b.ops {
		op := &b.ops[i]
		start := len(buf)
		if op.tombstone {
			buf = appendTombstoneEntry(buf, now, []byte(op.key))
		} else {
			buf = appendValueEntry(buf, now, op.expires, []byte(op.key), op.value)
			logical += uint64(len(op.key) + len(op.value))
		}
		sizes = append(sizes, uint32(len(buf)-start))
	}

	if err := e.appendDurable(active.f, buf); err != nil {
		return err
	}
	e.bytesWritten.Add(logical)

	// One index publication: under mu, in staging order, so later ops
	// in the batch win over earlier ones exactly as sequential single
	// writes would.
	e.mu.Lock()
	off := offset
	for i := range b.ops {
		op := &b.ops[i]
		size := sizes[i]
		if op.tombstone {
			if old, ok := e.index[op.key]; ok {
				e.liveBytes -= uint64(old.size)
				delete(e.index, op.key)
			}
		} else {
			if old, ok := e.index[op.key]; ok {
				e.liveBytes -= uint64(old.size)
			}
			e.index[op.key] = entryRef{fileID: e.activeID, offset: off, size: size}
			e.liveBytes += uint64(size)
		}
		off += int64(size)
	}
	active.entries += uint64(len(b.ops))
	active.bytes += int64(len(buf))
	e.activeOffset += int64(len(buf))
	rotate := e.activeOffset > int64(maxSize)
	e.mu.Unlock()

	b.ops = b.ops[:0] // the batch is spent; reuse starts empty
	if rotate {
		return e.rotateActive() // the batch is already durable; rotation only moves future appends
	}
	return nil
}

// Package engine implements Elena's embedded key-value storage engine.
//
// The engine follows the Bitcask model: an append-only data file per
// directory plus an in-memory index. This file provides the lifecycle
// surface: Open acquires a storage directory, holds an exclusive lock
// on it, and prepares the active data file; Close releases everything
// the engine holds.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// ErrDirectoryLocked is returned by Open when the target directory is
// already held by another open Engine, in this or another process.
// Opening the same directory twice is structurally forbidden so two
// writers can never corrupt the stored data.
var ErrDirectoryLocked = errors.New("engine: directory already locked by an open engine")

// lockFileName is the name of the advisory lock file created inside the
// storage directory. The lock is held via flock(2) for the lifetime of
// the Engine and released on Close.
const lockFileName = "LOCK"

// dataFileExt is the extension of data files. A data file name is the
// zero-padded decimal file ID plus this extension; zero padding keeps
// lexicographic order equal to numeric order.
const dataFileExt = ".log"

// dataFileName returns the data file name for a file ID.
func dataFileName(id uint32) string {
	return fmt.Sprintf("%010d%s", id, dataFileExt)
}

// Default limits for key and value sizes. Keys live entirely in the
// in-memory index, so they must stay small; Bitcask-style engines
// shine on larger values but the value still needs a hard ceiling so
// append buffers stay bounded. Both limits are configurable but
// mandatory: there is no "unlimited" setting.
const (
	defaultMaxKeySize   = 1 << 10 // 1 KiB
	defaultMaxValueSize = 4 << 20 // 4 MiB

	// defaultMaxDataFileSize keeps files large enough that per-file
	// costs (open, hint write, recovery scan) amortize over many
	// entries, yet bounded so merge and recovery work in file-sized
	// chunks instead of over one ever-growing file.
	defaultMaxDataFileSize = 64 << 20 // 64 MiB
)

// Options configures an Engine at Open time. The zero value is valid
// and selects the defaults.
type Options struct {
	// MaxKeySize is the largest accepted key length in bytes. Values
	// <= 0 select defaultMaxKeySize.
	MaxKeySize int

	// MaxValueSize is the largest accepted value length in bytes.
	// Values <= 0 select defaultMaxValueSize.
	MaxValueSize int

	// MaxDataFileSize is the size threshold that triggers rotation of
	// the active data file: once the active file grows beyond this many
	// bytes, it is closed as an immutable file and a new active file
	// with the next monotonic file ID takes over. Values <= 0 select
	// defaultMaxDataFileSize.
	MaxDataFileSize int

	// MergeDeadRatio is the dead-byte share (dead bytes / data bytes,
	// the Stats.DeadRatio quantity) at which the engine merges by
	// itself: the first write that pushes the ratio past the threshold
	// runs a merge before it returns. The cost of a merge therefore
	// lands on the write that crossed the threshold, while every other
	// writer keeps appending. A failed auto merge is dropped (the
	// triggering write already succeeded) and retried by the next
	// threshold-crossing write. Values <= 0 disable auto merge (the
	// default); values > 1 are rejected at Open because the dead share
	// can never exceed 1. Exactly 1 merges whenever any dead byte
	// exists.
	MergeDeadRatio float64
}

// dataFile couples an open data file with the per-file accounting
// Stats publishes and the handle lifecycle a merge interacts with.
//
// The entries count covers every encoded entry the file holds, live
// and dead; it is guarded by Engine.mu (appends count under mu in the
// write path, replay counts at Open).
//
// Handle lifecycle: while the file is in the engine's file set, Gets
// read through it under mu and Scans may snapshot it. A merge that
// reclaims the file marks it retired and removes it from the file set
// (under mu, so new snapshots stop referring to it), but an in-flight
// Scan may still be reading through the handle — the scanRefs
// counter tracks those readers, and the handle closes exactly once,
// when the file is retired and the last reader is done (or at Close
// at the latest).
type dataFile struct {
	f *os.File

	// entries and bytes are this file's entry count and size in bytes
	// (live and dead entries alike); both are guarded by Engine.mu and
	// maintained incrementally on append, at replay, and at merge.
	entries uint64
	bytes   int64

	scanRefs  atomic.Int32
	retired   atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// acquire registers one in-flight scan reader; called under mu while
// the file is still in the engine's file set.
func (df *dataFile) acquire() { df.scanRefs.Add(1) }

// release drops one in-flight scan reader and closes the handle if it
// just became unused and the file was retired meanwhile.
func (df *dataFile) release() {
	if df.scanRefs.Add(-1) == 0 && df.retired.Load() {
		_ = df.close()
	}
}

// close closes the handle exactly once and reports the (cached)
// result of that first close.
func (df *dataFile) close() error {
	df.closeOnce.Do(func() { df.closeErr = df.f.Close() })
	return df.closeErr
}

// Engine is an embedded key-value storage engine rooted at a directory.
//
// Concurrency model: single writer, many readers. writeMu serializes
// appends to the active data file and the index publication order that
// decides which entry wins; mu guards the index, the closed flag, and
// reads of the data files. Lock order is always writeMu before mu.
type Engine struct {
	dir      string
	lockFile *os.File

	files    map[uint32]*dataFile // open data files, keyed by file ID
	activeID uint32               // file that receives appends; fixed after Open

	activeOffset int64 // end of the active file; written only under writeMu

	maxKeySize      int
	maxValueSize    int
	maxDataFileSize int
	mergeDeadRatio  float64

	index map[string]entryRef

	// liveBytes is the encoded size of the entries the index points at,
	// maintained incrementally (write path, replay, merge swap) so the
	// dead share is computable in O(files) instead of O(keys). Guarded
	// by mu.
	liveBytes uint64

	// Cumulative observables, published through Stats. The op counters
	// tick on every public call; the byte counters tick only when bytes
	// are actually accepted or appended. They are atomics so Stats can
	// read them without holding the write lock.
	opsPut        atomic.Uint64
	opsPutWithTTL atomic.Uint64
	opsDelete     atomic.Uint64
	opsGet        atomic.Uint64
	opsMerge      atomic.Uint64
	bytesWritten  atomic.Uint64 // logical key+value bytes accepted
	bytesFlushed  atomic.Uint64 // encoded entry bytes appended

	// mergeMu serializes Merge calls; retired holds the handles of
	// merged-away files that an in-flight scan still reads through
	// (each closes when its last reader is done; Close is the
	// backstop). Lock order: mergeMu before writeMu before mu.
	mergeMu sync.Mutex
	retired []*dataFile

	writeMu sync.Mutex
	mu      sync.RWMutex
	closed  bool
}

// Open opens the storage directory rooted at dir, creating it (and any
// missing parents) if needed, acquires an exclusive lock on the
// directory, replays all existing data files into the in-memory index
// (truncating any torn tail), and prepares the active data file for
// appends.
func Open(dir string, opts Options) (*Engine, error) {
	if opts.MergeDeadRatio > 1 {
		return nil, fmt.Errorf("engine: open %q: Options.MergeDeadRatio %v is above 1 (the dead share cannot exceed 1)", dir, opts.MergeDeadRatio)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: create storage directory %q: %w", dir, err)
	}

	lockPath := filepath.Join(dir, lockFileName)
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("engine: open lock file %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("engine: open %q: %w", dir, ErrDirectoryLocked)
		}
		return nil, fmt.Errorf("engine: lock %q: %w", lockPath, err)
	}

	eng := &Engine{
		dir:             dir,
		lockFile:        lockFile,
		files:           make(map[uint32]*dataFile),
		maxKeySize:      opts.MaxKeySize,
		maxValueSize:    opts.MaxValueSize,
		maxDataFileSize: opts.MaxDataFileSize,
		mergeDeadRatio:  opts.MergeDeadRatio,
		index:           make(map[string]entryRef),
	}
	if eng.maxKeySize <= 0 {
		eng.maxKeySize = defaultMaxKeySize
	}
	if eng.maxValueSize <= 0 {
		eng.maxValueSize = defaultMaxValueSize
	}
	if eng.maxDataFileSize <= 0 {
		eng.maxDataFileSize = defaultMaxDataFileSize
	}

	// Hint publication renames a synced ".hint.tmp" into place, so a
	// crash mid-write leaves residue under the tmp name. Remove it
	// before recovery so the directory Open hands back is clean.
	if err := removeHintTmpResidue(dir); err != nil {
		_ = lockFile.Close()
		return nil, err
	}

	if err := eng.recoverFiles(); err != nil {
		_ = eng.Close()
		return nil, err
	}

	// recoverFiles loaded files in ascending ID order, so the last one
	// is the active file; a fresh directory starts at file 1.
	eng.activeID = uint32(1)
	for id := range eng.files {
		if id >= eng.activeID {
			eng.activeID = id
		}
	}
	if len(eng.files) == 0 {
		if err := eng.createActive(eng.activeID); err != nil {
			_ = eng.Close()
			return nil, err
		}
	}

	return eng, nil
}

// syncDir fsyncs the directory itself so newly created file entries
// inside it are durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("engine: open directory %q for sync: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("engine: sync directory %q: %w", dir, err)
	}
	return nil
}

// listDataFileIDs returns the sorted numeric IDs of all data files in
// dir. Files that do not match the data file naming scheme are
// ignored.
func listDataFileIDs(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("engine: list %q: %w", dir, err)
	}
	var ids []uint32
	for _, entry := range entries {
		id, ok := parseDataFileName(entry.Name())
		if ok {
			ids = append(ids, id)
		}
	}
	// Zero-padded names sort numerically, but sort by value anyway so
	// the invariant does not depend on naming alone.
	slices.Sort(ids)
	return ids, nil
}

// parseDataFileName reports whether name is a data file name and
// returns its ID.
func parseDataFileName(name string) (uint32, bool) {
	suffix := strings.TrimSuffix(name, dataFileExt)
	if suffix == name {
		return 0, false
	}
	id, err := strconv.ParseUint(suffix, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(id), true
}

// Close releases all resources held by the Engine, including the
// directory lock. It is safe to call Close more than once.
func (e *Engine) Close() error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true

	var firstErr error
	// The active file's contents are final now (single writer holds
	// writeMu through Close), so publish its hint while the file handle
	// is still open. A hint failure does not prevent the release of
	// resources; Open falls back to scanning the data file.
	if df := e.files[e.activeID]; df != nil {
		if err := e.writeHintFile(e.activeID, df.f); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for id, df := range e.files {
		if err := df.close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("engine: close data file %d: %w", id, err)
		}
		delete(e.files, id)
	}
	for _, df := range e.retired {
		if err := df.close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("engine: close retired data file: %w", err)
		}
	}
	e.retired = nil

	if err := syscall.Flock(int(e.lockFile.Fd()), syscall.LOCK_UN); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("engine: unlock %q: %w", filepath.Join(e.dir, lockFileName), err)
		}
	}
	if err := e.lockFile.Close(); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("engine: close lock file: %w", err)
		}
	}
	return firstErr
}

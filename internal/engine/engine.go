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

// Options configures an Engine at Open time. The zero value is valid.
type Options struct{}

// Engine is an embedded key-value storage engine rooted at a directory.
//
// Concurrency model: single writer, many readers. writeMu serializes
// appends to the active data file and the index publication order that
// decides which entry wins; mu guards the index, the closed flag, and
// reads of the active file. Lock order is always writeMu before mu.
type Engine struct {
	dir      string
	lockFile *os.File

	active       *os.File
	activeOffset int64 // end of the active file; written only under writeMu

	index map[string]entryRef

	writeMu sync.Mutex
	mu      sync.RWMutex
	closed  bool
}

// Open opens the storage directory rooted at dir, creating it (and any
// missing parents) if needed, acquires an exclusive lock on the
// directory, and prepares the active data file for appends.
func Open(dir string, opts Options) (*Engine, error) {
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
		dir:      dir,
		lockFile: lockFile,
		index:    make(map[string]entryRef),
	}

	ids, err := listDataFileIDs(dir)
	if err != nil {
		_ = eng.Close()
		return nil, err
	}
	activeID := uint32(1)
	if len(ids) > 0 {
		activeID = ids[len(ids)-1]
	}
	if err := eng.openActive(dir, activeID); err != nil {
		_ = eng.Close()
		return nil, err
	}
	return eng, nil
}

// openActive opens (creating on first use) the data file with the
// given ID in append mode and positions activeOffset at its end.
// Creating a brand-new file also syncs the directory entry, so the
// file is guaranteed to survive a crash before the first Put ack.
func (e *Engine) openActive(dir string, id uint32) error {
	path := filepath.Join(dir, dataFileName(id))
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	} else if err != nil {
		return fmt.Errorf("engine: stat data file %q: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("engine: open data file %q: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("engine: stat data file %q: %w", path, err)
	}

	e.active = f
	e.activeOffset = info.Size()

	if created {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("engine: sync new data file %q: %w", path, err)
		}
		if err := syncDir(dir); err != nil {
			_ = f.Close()
			return err
		}
	}
	return nil
}

// syncDir fsyncs the directory itself so newly created file entries
// inside it are durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("engine: open directory %q for sync: %w", dir, err)
	}
	defer d.Close()
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
	if e.active != nil {
		if err := e.active.Close(); err != nil {
			firstErr = fmt.Errorf("engine: close data file: %w", err)
		}
		e.active = nil
	}

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

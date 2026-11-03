// Package engine implements Elena's embedded key-value storage engine.
//
// The engine follows the Bitcask model: an append-only data file per
// directory plus an in-memory index. This file provides the lifecycle
// surface: Open acquires a storage directory and holds an exclusive
// lock on it; Close releases everything the engine holds.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Options configures an Engine at Open time. The zero value is valid.
type Options struct{}

// Engine is an embedded key-value storage engine rooted at a directory.
type Engine struct {
	dir      string
	lockFile *os.File

	mu     sync.Mutex
	closed bool
}

// Open opens the storage directory rooted at dir, creating it (and any
// missing parents) if needed, and acquires an exclusive lock on the
// directory.
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
		lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("engine: open %q: %w", dir, ErrDirectoryLocked)
		}
		return nil, fmt.Errorf("engine: lock %q: %w", lockPath, err)
	}

	return &Engine{dir: dir, lockFile: lockFile}, nil
}

// Close releases all resources held by the Engine, including the
// directory lock. It is safe to call Close more than once.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true

	if err := syscall.Flock(int(e.lockFile.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("engine: unlock %q: %w", filepath.Join(e.dir, lockFileName), err)
	}
	if err := e.lockFile.Close(); err != nil {
		return fmt.Errorf("engine: close lock file: %w", err)
	}
	return nil
}

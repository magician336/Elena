// Package semantic implements the semantic layer of Elena: sessions
// grouped by namespace, each owning one append-only trajectory of
// numbered events. Everything the layer stores goes through the
// key-value engine's public API — the layer performs no file I/O of
// its own and imports nothing else from the repository, so the engine
// underneath is replaceable in principle and in fact.
package semantic

import (
	"errors"
	"fmt"

	"elena/internal/engine"
)

// ErrDirectoryLocked is returned by Open when the target directory is
// already held by another open store, in this or another process. The
// engine's exclusive directory lock is inherited as-is: two stores can
// never share a directory, so two writers can never corrupt one.
var ErrDirectoryLocked = errors.New("elena: directory already locked by an open store")

// ErrClosed is returned by store operations attempted after Close.
var ErrClosed = errors.New("elena: store is closed")

// Store is a trajectory store rooted at one directory and driving one
// engine instance. All its methods are safe for concurrent use.
type Store struct {
	eng *engine.Engine
}

// Open opens the store rooted at dir, creating the directory if
// needed. The store holds an exclusive lock on the directory for its
// lifetime; a second Open of the same directory fails with an error
// wrapping ErrDirectoryLocked until Close releases it. Events
// acknowledged before Close return are durable: the engine runs with
// its strictest sync policy.
func Open(dir string) (*Store, error) {
	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		if errors.Is(err, engine.ErrDirectoryLocked) {
			return nil, fmt.Errorf("elena: open %q: %w", dir, ErrDirectoryLocked)
		}
		return nil, fmt.Errorf("elena: open %q: %w", dir, err)
	}
	return &Store{eng: eng}, nil
}

// Close closes the store and releases its directory lock. It is safe
// to call more than once; repeated calls are no-ops.
func (s *Store) Close() error {
	return s.eng.Close()
}

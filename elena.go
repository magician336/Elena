// Package elena provides an embedded state store: sessions grouped by
// namespace own append-only trajectories of numbered, timestamped
// events, durably stored in a single directory.
package elena

import (
	"elena/internal/semantic"
)

// Store is a trajectory store rooted at one directory. All methods are
// safe for use from multiple goroutines.
type Store struct {
	store *semantic.Store
}

// Open opens the store rooted at dir, creating the directory if
// needed. The store holds an exclusive lock on its directory: while it
// is open, a second Open of the same directory fails with an error
// wrapping ErrDirectoryLocked.
func Open(dir string) (*Store, error) {
	store, err := semantic.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Store{store: store}, nil
}

// Close closes the store and releases its directory lock. Events
// acknowledged before Close returns are durable. It is safe to call
// Close more than once; repeated calls are no-ops.
func (s *Store) Close() error {
	return s.store.Close()
}

// ErrDirectoryLocked is wrapped into the error Open returns when the
// directory is already held by another open store.
var ErrDirectoryLocked = semantic.ErrDirectoryLocked

// ErrClosed is wrapped into the errors of operations attempted after
// Close.
var ErrClosed = semantic.ErrClosed

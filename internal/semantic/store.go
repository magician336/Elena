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
	"sync"
	"time"

	"elena/internal/engine"
)

// ErrDirectoryLocked is returned by Open when the target directory is
// already held by another open store, in this or another process. The
// engine's exclusive directory lock is inherited as-is: two stores can
// never share a directory, so two writers can never corrupt one.
var ErrDirectoryLocked = errors.New("elena: directory already locked by an open store")

// ErrClosed is returned by store operations attempted after Close.
var ErrClosed = errors.New("elena: store is closed")

// DefaultSweepPeriod is the period of the background sweep when a Config
// leaves it unset: the sweep checks its deadline view once per minute.
const DefaultSweepPeriod = time.Minute

// ErrInvalidSweepPeriod is returned by Open when the configured sweep
// period cannot define a check interval: negative when the sweep is
// enabled. (The zero value means "unset, use the default"; an explicit
// zero is rejected one layer up, where it cannot be confused with an
// absent option.)
var ErrInvalidSweepPeriod = errors.New("elena: sweep period must be positive")

// Config configures the store at Open. The zero value is valid and
// selects the defaults.
type Config struct {
	// SweepPeriod is the period at which the background sweep checks
	// the deadline view of the timed sessions. Zero selects
	// DefaultSweepPeriod; negative is rejected with an error wrapping
	// ErrInvalidSweepPeriod. Ignored when SweepDisabled is set.
	SweepPeriod time.Duration

	// SweepDisabled turns the background sweep off: expiration is then
	// purely lazy — elapsed sessions are invisible immediately, and
	// their bytes come back only when some merge runs (a manual Merge,
	// or any signal another Open's sweep fires).
	SweepDisabled bool
}

// Store is a trajectory store rooted at one directory and driving one
// engine instance. All its methods are safe for concurrent use.
type Store struct {
	eng *engine.Engine

	// mu is the semantic write lock. It turns sequence allocation plus
	// the append into one atomic step, so the sequence numbers handed
	// out can never diverge from the order events reach the disk. It
	// also guards the per-trajectory runtime state in trajectories.
	// Reads take no semantic lock at all: they walk the engine's
	// concurrent scan and get paths.
	mu sync.Mutex

	// trajectories holds the runtime state of every session attached
	// through this Store, keyed by namespace and session ID.
	trajectories map[trajKey]*trajectory

	// deadlines is the sweep's deadline view: the pending deadline of
	// every timed session this store owes a merge for, keyed by
	// namespace and session ID. Perpetual sessions never enter it. It is
	// guarded by mu; entries leave when their deadline elapses and a
	// merge — the sweep's or a manual Merge call — consumes the signal.
	deadlines map[trajKey]time.Time
}

// Open opens the store rooted at dir, creating the directory if
// needed, under the configuration cfg. The store holds an exclusive
// lock on the directory for its lifetime; a second Open of the same
// directory fails with an error wrapping ErrDirectoryLocked until Close
// releases it. Events acknowledged before Close return are durable: the
// engine runs with its strictest sync policy.
func Open(dir string, cfg Config) (*Store, error) {
	if !cfg.SweepDisabled && cfg.SweepPeriod < 0 {
		return nil, fmt.Errorf("elena: open %q: sweep period %d: %w", dir, cfg.SweepPeriod, ErrInvalidSweepPeriod)
	}
	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		if errors.Is(err, engine.ErrDirectoryLocked) {
			return nil, fmt.Errorf("elena: open %q: %w", dir, ErrDirectoryLocked)
		}
		return nil, fmt.Errorf("elena: open %q: %w", dir, err)
	}
	return &Store{
		eng:          eng,
		trajectories: make(map[trajKey]*trajectory),
		deadlines:    make(map[trajKey]time.Time),
	}, nil
}

// Close closes the store and releases its directory lock. It is safe
// to call more than once; repeated calls are no-ops.
func (s *Store) Close() error {
	return s.eng.Close()
}

// opErr adapts an engine error to the semantic layer: a closed engine
// becomes ErrClosed; any other error keeps its identity and gains the
// operation's context.
func opErr(op string, err error) error {
	if errors.Is(err, engine.ErrClosed) {
		return fmt.Errorf("elena: %s: %w", op, ErrClosed)
	}
	return fmt.Errorf("elena: %s: %w", op, err)
}

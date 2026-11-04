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
	"sync/atomic"
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

	// SyncPolicy selects when appended entries are fdatasync'ed — the
	// durability/throughput trade-off, fixed for the lifetime of the
	// store this Open creates. The zero value selects SyncAlways, the
	// default; SyncInterval additionally requires a positive SyncPeriod
	// (Open rejects it with an error wrapping ErrInvalidSyncPeriod
	// otherwise).
	SyncPolicy SyncPolicy

	// SyncPeriod is the group-commit period of the SyncInterval policy:
	// the background flusher fdatasyncs the active data file once per
	// period. Ignored by the other policies.
	SyncPeriod time.Duration
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

	// sweepStop is closed by Close to tell the sweep goroutine to exit;
	// sweepDone is closed by that goroutine on exit (or at Open when the
	// sweep is disabled), and Close joins on it — the sweep goroutine's
	// exit is deterministic, never leaked.
	sweepStop chan struct{}
	sweepDone chan struct{}

	// sweepErr holds the first merge failure of the background sweep, to
	// be surfaced by Close: a background goroutine has no caller to
	// report to at the moment of failure.
	sweepErr atomic.Pointer[error]

	// closeOnce makes Close idempotent; closeErr carries its result for
	// the repeated calls.
	closeOnce sync.Once
	closeErr  error
}

// Open opens the store rooted at dir, creating the directory if
// needed, under the configuration cfg. The store holds an exclusive
// lock on the directory for its lifetime; a second Open of the same
// directory fails with an error wrapping ErrDirectoryLocked until Close
// releases it. Events acknowledged before Close return are durable
// under the default sync policy: the engine runs with the configured
// policy — always, its strictest tier, by default.
func Open(dir string, cfg Config) (*Store, error) {
	if !cfg.SweepDisabled && cfg.SweepPeriod < 0 {
		return nil, fmt.Errorf("elena: open %q: sweep period %d: %w", dir, cfg.SweepPeriod, ErrInvalidSweepPeriod)
	}
	if cfg.SyncPolicy == SyncInterval && cfg.SyncPeriod <= 0 {
		return nil, fmt.Errorf("elena: open %q: sync period %d: %w", dir, cfg.SyncPeriod, ErrInvalidSyncPeriod)
	}
	eng, err := engine.Open(dir, engine.Options{
		SyncPolicy: cfg.SyncPolicy,
		SyncPeriod: cfg.SyncPeriod,
	})
	if err != nil {
		if errors.Is(err, engine.ErrDirectoryLocked) {
			return nil, fmt.Errorf("elena: open %q: %w", dir, ErrDirectoryLocked)
		}
		return nil, fmt.Errorf("elena: open %q: %w", dir, err)
	}
	s := &Store{
		eng:          eng,
		trajectories: make(map[trajKey]*trajectory),
		deadlines:    make(map[trajKey]time.Time),
		sweepStop:    make(chan struct{}),
		sweepDone:    make(chan struct{}),
	}
	if err := s.collectDeadlines(); err != nil {
		_ = eng.Close()
		return nil, err
	}
	s.startSweep(cfg)
	return s, nil
}

// startSweep launches the background sweep unless the configuration
// disabled it. With the sweep disabled, sweepDone is closed here so
// Close's join finds it ready and no goroutine ever exists.
func (s *Store) startSweep(cfg Config) {
	if cfg.SweepDisabled {
		close(s.sweepDone)
		return
	}
	period := cfg.SweepPeriod
	if period == 0 {
		period = DefaultSweepPeriod
	}
	go s.sweepForever(period)
}

// Close closes the store and releases its directory lock. It is safe
// to call more than once; repeated calls are no-ops. The background
// sweep, if running, exits before the engine closes — its goroutine is
// joined, not abandoned — and the first sweep failure, if any, is
// reported here.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.sweepStop)
		<-s.sweepDone
		var err error
		if p := s.sweepErr.Load(); p != nil {
			err = *p
		}
		if cerr := s.eng.Close(); err == nil {
			err = cerr
		}
		s.closeErr = err
	})
	return s.closeErr
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

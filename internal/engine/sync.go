package engine

import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

// SyncPolicy selects when appended entries are fdatasync'ed to disk —
// the explicit durability/throughput trade-off of the engine. The
// policy is an Open-time option; it cannot change during the lifetime
// of an Engine instance.
//
// Loss windows, stated per tier (process-crash and power-loss are
// different faults: SIGKILL leaves the OS page cache alive, so dirty
// pages of a killed process still reach the disk; only a machine-level
// fault loses them):
//
//   - SyncAlways: every write is fdatasync'ed before it acknowledges.
//     An acknowledged write is durable against any fault. Highest
//     latency per write; this is the default.
//   - SyncInterval: writes acknowledge after the append alone; a
//     background flusher fdatasyncs the active file once per configured
//     period (Options.SyncPeriod). An acknowledged write is durable
//     after the flush boundary that follows it — at most one period of
//     acknowledged writes can be lost to a machine-level fault. The
//     final flush is forced at Close and at every rotation, so an
//     engine closed or rotated away has no open window. A process crash
//     (SIGKILL) loses nothing: the bytes are already in the page cache.
//   - SyncNone: the engine never fdatasyncs the data files. The loss
//     window is the whole OS page cache: a machine-level fault can lose
//     every write that the kernel has not written back on its own —
//     including writes from long-closed files. No durability claim is
//     made beyond "the bytes are visible to readers of the same
//     running engine". Structural metadata (hint publication, merge
//     output, file creation) is still synced: what is traded away is
//     data-entry durability, not recovery integrity — a reopened
//     directory always replays to a consistent index.
type SyncPolicy int

const (
	// SyncAlways is the default policy: fdatasync before every write
	// acknowledges ("ack means durable").
	SyncAlways SyncPolicy = iota

	// SyncInterval is group commit: a background flusher syncs the
	// active file once per Options.SyncPeriod. Writes acknowledge
	// without waiting; the acknowledged-but-unsynced window is at most
	// one period, and it is closed by Close and by rotation.
	SyncInterval

	// SyncNone never syncs the data files: maximum throughput, the loss
	// window is the OS page cache (see the SyncPolicy doc comment).
	SyncNone
)

// defaultSyncPeriod is the group-commit period used when
// Options.SyncPeriod is unset for the SyncInterval policy. Milliseconds
// at this scale amortize sync latency over many writes without making
// the loss window feel unbounded in operation.
const defaultSyncPeriod = 10 * time.Millisecond

// String returns the policy's name for configuration error messages.
func (p SyncPolicy) String() string {
	switch p {
	case SyncAlways:
		return "always"
	case SyncInterval:
		return "interval"
	case SyncNone:
		return "none"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// validate reports whether p is one of the defined policies.
func (p SyncPolicy) validate() error {
	switch p {
	case SyncAlways, SyncInterval, SyncNone:
		return nil
	default:
		return fmt.Errorf("engine: Options.SyncPolicy %s is not one of always, interval, none", p)
	}
}

// syncDataFile fdatasyncs one data file handle. The caller must hold
// writeMu (directly or through a path that serializes with appends and
// handle closes): a raw syscall.Fdatasync on a closed fd is undefined,
// and os.File's refcount does not guard raw syscalls, so the lock — not
// the handle — is what makes this safe.
func syncDataFile(f *os.File) error {
	if err := syscall.Fdatasync(int(f.Fd())); err != nil {
		return fmt.Errorf("engine: fdatasync data file: %w", err)
	}
	return nil
}

// groupSyncer is the background flusher of the SyncInterval policy: one
// goroutine that fdatasyncs the active data file once per interval,
// batching the durability cost of every write that landed since the
// previous tick.
//
// Synchronization model: the flusher's fdatasync runs under writeMu
// (never a bare mu) so it is mutually exclusive with every path that
// appends, rotates, merges, or closes file handles — the fd it syncs
// cannot be closed concurrently. The cost is that writers can briefly
// queue behind one flush; that is the group-commit bargain (N writes
// share one sync) and it is why the write path itself never waits for a
// sync under this policy.
type groupSyncer struct {
	eng      *Engine
	interval time.Duration

	stop chan struct{} // closed by Close: the loop exits on next select
	done chan struct{} // closed by the loop on exit: Close joins on it

	// flushErr holds the first background fdatasync failure. A failed
	// flush is a durability regression the engine cannot repair on its
	// own, so it is remembered and reported by Close — the one moment
	// an operator is guaranteed to look.
	flushErr atomic.Pointer[error]
}

// newGroupSyncer creates the flusher for eng; start it with run in a
// fresh goroutine right after Open has fully succeeded.
func newGroupSyncer(eng *Engine, interval time.Duration) *groupSyncer {
	return &groupSyncer{
		eng:      eng,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// run is the flusher loop: every interval it syncs the active file once
// (a no-op once the engine is closed). It exits when stop is closed and
// signals its exit through done, touching no engine state on the way
// out — which is why Close may keep joining even after every handle is
// already closed.
func (s *groupSyncer) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.flushOnce()
		}
	}
}

// flushOnce fdatasyncs the current active file under writeMu. On a
// closed engine it is a no-op: the flusher may be one tick behind
// Close, and by then every handle it could touch is closed anyway —
// checking e.closed inside the locks keeps the last tick harmless. A
// sync failure is remembered for Close to report.
func (s *groupSyncer) flushOnce() {
	e := s.eng
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.RLock()
	closed := e.closed
	var active *os.File
	if !closed {
		if df := e.files[e.activeID]; df != nil {
			active = df.f
		}
	}
	e.mu.RUnlock()
	if active == nil {
		return
	}
	if err := syncDataFile(active); err != nil && s.flushErr.Load() == nil {
		s.flushErr.Store(&err)
	}
}

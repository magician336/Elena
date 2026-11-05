package elena

import (
	"fmt"
	"time"

	"github.com/magician336/elena/internal/semantic"
)

// DefaultSweepPeriod is the period of the background sweep when Open is
// called without an explicit sweep option: the sweep checks its deadline
// view once per minute by default.
const DefaultSweepPeriod = semantic.DefaultSweepPeriod

// OpenOption configures a store at Open time. Options apply in the
// order given; where two options configure the same behavior, the last
// one wins.
type OpenOption func(*openOptions)

// openOptions accumulates the Open options into the semantic layer's
// configuration. sweepPeriodSet distinguishes "caller asked for this
// period" from "caller said nothing", so an explicit zero is rejected
// instead of being read as the default. The sync fields hold the last
// sync option given; the interval tier carries its period with it, so
// no set flag is needed there.
type openOptions struct {
	sweepPeriod    time.Duration
	sweepPeriodSet bool
	sweepDisabled  bool
	syncPolicy     semantic.SyncPolicy
	syncPeriod     time.Duration
}

// config validates the accumulated options and returns the semantic
// layer's configuration they amount to. The last sweep option wins, so
// a period is validated only when it is what the store will use; the
// last sync option wins the same way, and its period is validated by
// the semantic layer, where the tier it belongs to is known.
func (o *openOptions) config() (semantic.Config, error) {
	if !o.sweepDisabled && o.sweepPeriodSet && o.sweepPeriod <= 0 {
		return semantic.Config{}, fmt.Errorf("sweep period %d: %w", o.sweepPeriod, ErrInvalidSweepPeriod)
	}
	return semantic.Config{
		SweepPeriod:   o.sweepPeriod,
		SweepDisabled: o.sweepDisabled,
		SyncPolicy:    o.syncPolicy,
		SyncPeriod:    o.syncPeriod,
	}, nil
}

// WithSweepPeriod sets the period of the background sweep: once per
// period the sweep checks its deadline view of the timed sessions and,
// when a deadline has elapsed, triggers one merge — the merge rewrites
// the live entries and omits the expired ones, returning their disk
// bytes and index memory. The period must be positive (an error
// wrapping ErrInvalidSweepPeriod from Open otherwise).
func WithSweepPeriod(period time.Duration) OpenOption {
	return func(o *openOptions) {
		o.sweepPeriod = period
		o.sweepPeriodSet = true
		o.sweepDisabled = false
	}
}

// WithSweepDisabled turns the background sweep off: no sweep goroutine
// exists, expiration is purely lazy. Elapsed sessions are invisible the
// moment their deadline passes, but their bytes come back only when
// some merge runs — a manual Merge call, or a sweep of a later Open on
// the same directory. Embedded hosts that want full control over
// background work want this; the default is the sweep on, one check per
// DefaultSweepPeriod.
func WithSweepDisabled() OpenOption {
	return func(o *openOptions) {
		o.sweepDisabled = true
	}
}

// WithSyncInterval selects group commit: an append acknowledges after
// the write alone, and a background flusher fdatasyncs the active data
// file once per period, so the sync cost of many appends is shared
// instead of paid per write. The period must be positive (an error
// wrapping ErrInvalidSyncPeriod from Open otherwise).
//
// Durability semantics: an acknowledged event becomes durable at the
// flush boundary that follows it — a machine-level fault can lose at
// most one period of acknowledged events. The final flush is forced at
// Close and at every rotation, so a store that was closed cleanly has
// no open window. A process crash (SIGKILL) loses nothing: the bytes
// are already in the OS page cache and still reach the disk. The
// policy is fixed at Open, for the lifetime of the store.
func WithSyncInterval(period time.Duration) OpenOption {
	return func(o *openOptions) {
		o.syncPolicy = semantic.SyncInterval
		o.syncPeriod = period
	}
}

// WithSyncAlways selects the default sync policy: every append is
// fdatasync'ed before it acknowledges, so an acknowledged event is
// durable against any fault — process crash or power loss alike. This
// is the highest-durability, highest-latency tier, and the one Open
// uses when no sync option is given; WithSyncAlways exists to override
// an earlier sync option.
func WithSyncAlways() OpenOption {
	return func(o *openOptions) {
		o.syncPolicy = semantic.SyncAlways
	}
}

// WithSyncNone selects no syncing of the data files: maximum
// throughput, and the loss window is the whole OS page cache — a
// machine-level fault can lose every event the kernel has not written
// back on its own, including events of stores closed long ago. No
// durability claim is made beyond this: an acknowledged event is
// visible to readers of the same running store. Structural metadata —
// hint publication, merge output, file creation — is still synced, so
// what the tier trades away is data-entry durability, never recovery
// integrity: a reopened directory always rebuilds a consistent index.
func WithSyncNone() OpenOption {
	return func(o *openOptions) {
		o.syncPolicy = semantic.SyncNone
	}
}

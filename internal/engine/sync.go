package engine

import (
	"fmt"
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

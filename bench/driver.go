package bench

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// driverNames lists every engine the registry resolves, in report
// order — the engine axis of the comparison matrix.
var driverNames = []string{"elena", "bbolt", "badger", "rosedb"}

// Drivers returns the names For and ForTier resolve, in report order.
func Drivers() []string {
	return append([]string(nil), driverNames...)
}

// For returns a fresh driver by its report name at the per-write sync
// tier — the one place new engines join the harness: add the driver,
// add its case, and every entry point (CLI, tests, matrix runners)
// reaches it by name. An unknown name is an error, not a default.
func For(name string) (Driver, error) {
	return ForTier(name, SyncPerWrite, 0)
}

// SyncTier declares one durability regime the harness aligns engines
// on. Cross-engine numbers exist only inside one tier: an every-write
// synchronized run and a group-committed run are not one comparison.
type SyncTier int

const (
	// SyncPerWrite aligns every engine at its every-write-synchronized
	// configuration: an acknowledged write is durable against power
	// loss by the time the operation returns.
	SyncPerWrite SyncTier = iota

	// SyncGroupCommit aligns at periodic synchronization: an
	// acknowledged write reaches the OS page cache immediately and a
	// background flusher synchronizes the store once per period, so a
	// machine-level fault can lose at most one period of acknowledged
	// writes. Only an engine exposing such a configuration runs this
	// tier; for every other engine ForTier answers ErrTierUnsupported
	// and the matrix produces no numbers for that combination.
	SyncGroupCommit
)

// String returns the tier's report name.
func (t SyncTier) String() string {
	switch t {
	case SyncPerWrite:
		return "per-write-sync"
	case SyncGroupCommit:
		return "group-commit"
	default:
		return fmt.Sprintf("sync-tier(%d)", int(t))
	}
}

// ErrTierUnsupported marks an engine and tier with no aligned
// durability configuration: the caller skips that combination loudly
// rather than timing a mismatched regime.
var ErrTierUnsupported = errors.New("sync tier unsupported by engine")

// ForTier returns a fresh driver by its report name, configured for the
// declared durability tier. SyncGroupCommit carries its period and is
// valid only with a positive one; an engine without a configuration
// matching the tier is an ErrTierUnsupported error — the combination
// the matrix skips on purpose, never silently.
func ForTier(name string, tier SyncTier, period time.Duration) (Driver, error) {
	switch name {
	case "elena":
		if tier == SyncGroupCommit && period <= 0 {
			return nil, fmt.Errorf("driver elena: group-commit period %d must be positive", period)
		}
		return &elenaDriver{syncTier: tier, syncPeriod: period}, nil
	case "bbolt":
		if tier != SyncPerWrite {
			return nil, fmt.Errorf("%w: bbolt has no periodic-sync configuration", ErrTierUnsupported)
		}
		return &bboltDriver{}, nil
	case "badger":
		if tier != SyncPerWrite {
			return nil, fmt.Errorf("%w: badger has no periodic-sync configuration", ErrTierUnsupported)
		}
		return &badgerDriver{}, nil
	case "rosedb":
		if tier != SyncPerWrite {
			return nil, fmt.Errorf("%w: rosedb has no periodic-sync configuration", ErrTierUnsupported)
		}
		return &rosedbDriver{}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", name)
	}
}

// Driver is one engine the harness can drive, behind a single
// lifecycle: open a fresh store under one directory, execute single
// operations, close. Drivers map the harness's logical operations onto
// their engine's own addressing; the elena driver goes through the
// public elena library only, competitor drivers through their official
// client packages.
type Driver interface {
	// Name identifies the driver in reports.
	Name() string

	// Open prepares a fresh store under dir, which exists.
	Open(dir string) error

	// Do executes one operation synchronously; returning marks the
	// operation complete — for writes, acknowledged as durable under
	// the driver's declared sync regime.
	Do(op Op) error

	// Close releases the store and its resources.
	Close() error
}

// kvKey renders one logical key as its 8-byte big-endian encoding —
// the common addressing every KV driver in the harness uses, the same
// width the official bbolt bench tool's keys carry, so one workload
// drives every KV driver over the identical byte keys.
func kvKey(key uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, key)
	return buf
}

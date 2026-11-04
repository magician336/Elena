package bench

import (
	"fmt"

	"github.com/rosedblabs/rosedb/v2"
)

// rosedbDriver drives rosedb through its official Go package. Mapping:
// one logical key is one 8-byte big-endian key — the same encoding
// every KV driver here uses — an append is a Put, a read a Get of the
// key's latest value.
//
// Durability: Sync is set to true — after every write the write-ahead
// log segment is fsynced before the write acknowledges, the
// configuration rosedb's option documentation scopes as required for
// durability of a single write. The rosedb default is false, so the
// driver sets the tier explicitly instead of riding the default; every
// other option keeps its default. rosedb's BytesPerSync flushes by
// accumulated bytes rather than by a time period, which is not the
// harness's group-commit tier, so that tier produces no rosedb numbers.
type rosedbDriver struct {
	db        *rosedb.DB
	latestLen map[uint64]int // key → payload length of its latest acknowledged put
}

// Name identifies the driver in reports.
func (d *rosedbDriver) Name() string { return "rosedb" }

// Open opens a fresh database under dir with the synchronized-writes
// tier pinned. DefaultOptions is copied field by field (Options is a
// plain value type) and the two fields this driver owns are overridden
// on the copy.
func (d *rosedbDriver) Open(dir string) error {
	opts := rosedb.DefaultOptions
	opts.DirPath = dir
	opts.Sync = true
	db, err := rosedb.Open(opts)
	if err != nil {
		return err
	}
	d.db = db
	d.latestLen = map[uint64]int{}
	return nil
}

// Do executes one operation: an append puts the value under the key's
// 8-byte encoding and records the payload length; a read gets the key
// back and verifies it — present, with the payload length of the
// latest put — so a read that misses the newest value fails the run
// instead of passing silently.
func (d *rosedbDriver) Do(op Op) error {
	switch op.Kind {
	case OpAppend:
		if err := d.db.Put(kvKey(op.Key), op.Value); err != nil {
			return err
		}
		d.latestLen[op.Key] = len(op.Value)
		return nil
	case OpRead:
		wantLen, ok := d.latestLen[op.Key]
		if !ok {
			return fmt.Errorf("read of key %d with no acknowledged append", op.Key)
		}
		value, err := d.db.Get(kvKey(op.Key))
		if err != nil {
			return fmt.Errorf("get key %d: %w", op.Key, err)
		}
		if len(value) != wantLen {
			return fmt.Errorf("read of key %d returned %d bytes, want %d", op.Key, len(value), wantLen)
		}
		return nil
	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// Close closes the database; operations after it fail with rosedb's
// closed-store errors.
func (d *rosedbDriver) Close() error {
	return d.db.Close()
}

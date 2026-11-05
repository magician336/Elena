package bench

import (
	"fmt"

	"github.com/rosedblabs/rosedb/v2"
)

// rosedbDriver drives rosedb through its official Go package. Mapping:
// one logical key is one 8-byte big-endian key — the same encoding
// every KV driver here uses — an append is one batch's Put, a read a
// Get of the key's latest value.
//
// Durability, per the rosedb v2 source (wal v1.3.8): the DB-level Sync
// flag only governs a write-ahead path the batch operations never
// ride, and db.Put runs its internal batch with Sync off — so no
// DB-level setting makes a Put fsync. The every-write-synchronized
// configuration that exists is a batch with Sync=true (its commit
// fsyncs the write-ahead log before returning), which requires the
// DB-level Sync flag to stay false — a batch's own sync is disabled
// otherwise. The driver therefore opens with Sync=false and executes
// every append as one Sync=true batch: an acknowledged append is
// fsynced. rosedb's BytesPerSync flushes by accumulated bytes rather
// than by a time period, which is not the harness's group-commit tier,
// so that tier produces no rosedb numbers.
type rosedbDriver struct {
	db    *rosedb.DB
	reads readLedger[int] // key → payload length of its latest acknowledged put
}

// Name identifies the driver in reports.
func (d *rosedbDriver) Name() string { return "rosedb" }

// Open opens a fresh database under dir. DefaultOptions is copied
// field by field (Options is a plain value type) and the two fields
// this driver owns are overridden on the copy: the directory, and Sync
// false — the per-write tier rides the batch-level sync, which the
// DB-level flag would disable.
func (d *rosedbDriver) Open(dir string) error {
	opts := rosedb.DefaultOptions
	opts.DirPath = dir
	opts.Sync = false
	db, err := rosedb.Open(opts)
	if err != nil {
		return err
	}
	d.db = db
	d.reads = newReadLedger[int]()
	return nil
}

// Do executes one operation: an append is one Sync=true batch — put,
// commit, and the fsync the commit performs — then records the payload
// length; a read gets the key back and verifies it — present, with the
// payload length of the latest put — so a read that misses the newest
// value fails the run instead of passing silently.
func (d *rosedbDriver) Do(op Op) error {
	switch op.Kind {
	case OpAppend:
		batch := d.db.NewBatch(rosedb.BatchOptions{Sync: true})
		if err := batch.Put(kvKey(op.Key), op.Value); err != nil {
			_ = batch.Rollback()
			return err
		}
		// No Rollback after Commit in any outcome: Commit releases the
		// batch's lock itself, success and failure alike.
		if err := batch.Commit(); err != nil {
			return err
		}
		d.reads.record(op.Key, len(op.Value))
		return nil
	case OpRead:
		wantLen, err := d.reads.expect(op.Key)
		if err != nil {
			return err
		}
		value, err := d.db.Get(kvKey(op.Key))
		if err != nil {
			return fmt.Errorf("get key %d: %w", op.Key, err)
		}
		return verifyReadLen(op.Key, value, wantLen)
	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// Close closes the database; operations after it fail with rosedb's
// closed-store errors.
func (d *rosedbDriver) Close() error {
	return d.db.Close()
}

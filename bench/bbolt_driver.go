package bench

import (
	"fmt"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bboltBucket is the one bucket the bbolt driver uses.
var bboltBucket = []byte("bench")

// bboltLockTimeout is how long Open waits on a database file another
// process holds before failing: short, because a contended benchmark
// directory is a configuration error worth surfacing fast.
const bboltLockTimeout = 5 * time.Second

// bboltDriver drives bbolt through its official Go package. Mapping:
// one logical key is one 8-byte big-endian bucket key — the common
// encoding every KV driver here uses, the same width the official
// bbolt bench tool's keys carry — an append is a bucket put, a read a
// bucket get.
//
// Durability: every append runs as its own write transaction with
// bbolt's default synchronization — a committed transaction is synced
// to disk before Do returns, the regime that lines up with the elena
// driver's write-synchronized appends, so the two drivers' numbers
// compare at one and the same durability level. Reads run in read
// transactions, which never sync.
type bboltDriver struct {
	db          *bolt.DB
	reads       readLedger[int] // key → payload length of its latest put
	lockTimeout time.Duration   // zero means the bbolt default (wait forever)
}

// Name identifies the driver in reports.
func (d *bboltDriver) Name() string { return "bbolt" }

// Open opens a fresh database file under dir and creates the bucket.
func (d *bboltDriver) Open(dir string) error {
	opts := &bolt.Options{Timeout: d.lockTimeout}
	if d.lockTimeout == 0 {
		opts.Timeout = bboltLockTimeout
	}
	db, err := bolt.Open(filepath.Join(dir, "bench.db"), 0o600, opts)
	if err != nil {
		return err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bboltBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return err
	}
	d.db = db
	d.reads = newReadLedger[int]()
	return nil
}

// Do executes one operation: an append puts the value under the key's
// 8-byte encoding and records the payload length; a read gets the key
// back and verifies it — present, with the payload length of the
// latest put — so a read that misses the newest value fails the run
// instead of passing silently.
func (d *bboltDriver) Do(op Op) error {
	switch op.Kind {
	case OpAppend:
		err := d.db.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(bboltBucket).Put(kvKey(op.Key), op.Value)
		})
		if err != nil {
			return err
		}
		d.reads.record(op.Key, len(op.Value))
		return nil
	case OpRead:
		wantLen, err := d.reads.expect(op.Key)
		if err != nil {
			return err
		}
		return d.db.View(func(tx *bolt.Tx) error {
			value := tx.Bucket(bboltBucket).Get(kvKey(op.Key))
			if value == nil {
				return fmt.Errorf("read of key %d returned nothing", op.Key)
			}
			return verifyReadLen(op.Key, value, wantLen)
		})
	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// Close closes the database; operations after it fail with bbolt's
// closed-database errors.
func (d *bboltDriver) Close() error {
	return d.db.Close()
}

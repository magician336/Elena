package bench

import (
	"fmt"

	badger "github.com/dgraph-io/badger/v4"
)

// silentLogger drops every badger log line: the harness's numbers are
// the report, and engine chatter on stderr is not part of any
// measurement path.
type silentLogger struct{}

func (silentLogger) Errorf(string, ...any)   {}
func (silentLogger) Warningf(string, ...any) {}
func (silentLogger) Infof(string, ...any)    {}
func (silentLogger) Debugf(string, ...any)   {}

// badgerDriver drives badger through its official Go package. Mapping:
// one logical key is one 8-byte big-endian key — the same encoding
// every KV driver here uses — an append is a write transaction's Set, a
// read a read transaction's Get of the key's latest version.
//
// Durability: SyncWrites is set to true — after every write badger
// msyncs the write-ahead value log, the configuration its own option
// documentation scopes as surviving hard reboots. The badger default is
// false (writes survive process crashes only), so the driver sets the
// tier explicitly instead of riding the default; every other option
// keeps its default. badger exposes no periodic-sync configuration, so
// the group-commit tier produces no badger numbers. Reads run in read
// transactions, which never sync.
type badgerDriver struct {
	db    *badger.DB
	reads readLedger[int] // key → payload length of its latest acknowledged set
}

// Name identifies the driver in reports.
func (d *badgerDriver) Name() string { return "badger" }

// Open opens a fresh database under dir with the synchronized-writes
// tier pinned.
func (d *badgerDriver) Open(dir string) error {
	opts := badger.DefaultOptions(dir).
		WithSyncWrites(true).
		WithLogger(silentLogger{})
	db, err := badger.Open(opts)
	if err != nil {
		return err
	}
	d.db = db
	d.reads = newReadLedger[int]()
	return nil
}

// Do executes one operation: an append sets the value under the key's
// 8-byte encoding and records the payload length; a read gets the key's
// latest version back and verifies it — present, with the payload
// length of the latest set — so a read that misses the newest value
// fails the run instead of passing silently.
func (d *badgerDriver) Do(op Op) error {
	switch op.Kind {
	case OpAppend:
		err := d.db.Update(func(txn *badger.Txn) error {
			return txn.Set(kvKey(op.Key), op.Value)
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
		return d.db.View(func(txn *badger.Txn) error {
			item, err := txn.Get(kvKey(op.Key))
			if err != nil {
				return fmt.Errorf("get key %d: %w", op.Key, err)
			}
			return item.Value(func(value []byte) error {
				return verifyReadLen(op.Key, value, wantLen)
			})
		})
	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// Close closes the database; operations after it fail with badger's
// closed-store errors.
func (d *badgerDriver) Close() error {
	return d.db.Close()
}

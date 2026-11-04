package bench

import (
	"testing"
)

// TestBadgerDriverName pins the report name.
func TestBadgerDriverName(t *testing.T) {
	if got := (&badgerDriver{}).Name(); got != "badger" {
		t.Fatalf("Name() = %q, want %q", got, "badger")
	}
}

// TestForReturnsBadgerDriver pins the registry case: the CLI name
// resolves to a fresh badger driver.
func TestForReturnsBadgerDriver(t *testing.T) {
	d, err := For("badger")
	if err != nil {
		t.Fatalf("For(badger): %v", err)
	}
	if d.Name() != "badger" {
		t.Fatalf("driver name = %q, want badger", d.Name())
	}
}

// TestBadgerDriverAppendReadRoundtrip drives the full lifecycle against
// a real database directory: two keys, two appends each — the second
// append overwriting the first, as badger keeps versions per key — then
// reads of both keys resolving to the latest values.
func TestBadgerDriverAppendReadRoundtrip(t *testing.T) {
	drv := &badgerDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = drv.Close() }()

	value := []byte("payload-bytes")
	for _, key := range []uint64{0, 1} {
		for i := 0; i < 2; i++ {
			if err := drv.Do(Op{Kind: OpAppend, Key: key, Value: value}); err != nil {
				t.Fatalf("append key %d round %d: %v", key, i, err)
			}
		}
	}
	for _, key := range []uint64{0, 1} {
		if err := drv.Do(Op{Kind: OpRead, Key: key}); err != nil {
			t.Fatalf("read key %d: %v", key, err)
		}
	}
}

// TestBadgerDriverReadBeforeAppendFails pins that reads verify against
// the store's state: a key never appended has nothing to read back,
// which fails the run instead of passing silently.
func TestBadgerDriverReadBeforeAppendFails(t *testing.T) {
	drv := &badgerDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = drv.Close() }()

	if err := drv.Do(Op{Kind: OpRead, Key: 42}); err == nil {
		t.Fatal("read of a never-appended key succeeded, want failure")
	}
}

// TestBadgerDriverAfterCloseFails pins the lifecycle boundary: once
// closed, operations surface badger's closed-store errors rather than
// pretending to succeed.
func TestBadgerDriverAfterCloseFails(t *testing.T) {
	drv := &badgerDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := drv.Do(Op{Kind: OpAppend, Key: 0, Value: []byte("v")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := drv.Do(Op{Kind: OpAppend, Key: 0, Value: []byte("v")}); err == nil {
		t.Fatal("append after Close succeeded, want failure")
	}
	if err := drv.Do(Op{Kind: OpRead, Key: 0}); err == nil {
		t.Fatal("read after Close succeeded, want failure")
	}
}

// TestBadgerDriverOpenLockedDirectory pins the failure path a second
// store on one directory takes: badger's directory lock is its own
// guard, and the driver surfaces it.
func TestBadgerDriverOpenLockedDirectory(t *testing.T) {
	dir := t.TempDir()
	holder := &badgerDriver{}
	if err := holder.Open(dir); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer func() { _ = holder.Close() }()

	second := &badgerDriver{}
	if err := second.Open(dir); err == nil {
		t.Fatal("second Open on the locked directory succeeded, want failure")
	}
}

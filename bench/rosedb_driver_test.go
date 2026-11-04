package bench

import (
	"testing"
)

// TestRosedbDriverName pins the report name.
func TestRosedbDriverName(t *testing.T) {
	if got := (&rosedbDriver{}).Name(); got != "rosedb" {
		t.Fatalf("Name() = %q, want %q", got, "rosedb")
	}
}

// TestForReturnsRosedbDriver pins the registry case: the CLI name
// resolves to a fresh rosedb driver.
func TestForReturnsRosedbDriver(t *testing.T) {
	d, err := For("rosedb")
	if err != nil {
		t.Fatalf("For(rosedb): %v", err)
	}
	if d.Name() != "rosedb" {
		t.Fatalf("driver name = %q, want rosedb", d.Name())
	}
}

// TestRosedbDriverAppendReadRoundtrip drives the full lifecycle against
// a real database directory: two keys, two appends each — the second
// append overwriting the first, as rosedb is an in-place update store —
// then reads of both keys resolving to the latest values.
func TestRosedbDriverAppendReadRoundtrip(t *testing.T) {
	drv := &rosedbDriver{}
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

// TestRosedbDriverReadBeforeAppendFails pins that reads verify against
// the index's state: a key never appended has no latest value, which
// fails the run instead of passing silently.
func TestRosedbDriverReadBeforeAppendFails(t *testing.T) {
	drv := &rosedbDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = drv.Close() }()

	if err := drv.Do(Op{Kind: OpRead, Key: 42}); err == nil {
		t.Fatal("read of a never-appended key succeeded, want failure")
	}
}

// TestRosedbDriverAfterCloseFails pins the lifecycle boundary: once
// closed, operations surface rosedb's closed-store errors rather than
// pretending to succeed.
func TestRosedbDriverAfterCloseFails(t *testing.T) {
	drv := &rosedbDriver{}
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

// TestRosedbDriverOpenLockedDirectory pins the failure path a second
// store on one directory takes: rosedb's directory lock is its own
// guard, and the driver surfaces it.
func TestRosedbDriverOpenLockedDirectory(t *testing.T) {
	dir := t.TempDir()
	holder := &rosedbDriver{}
	if err := holder.Open(dir); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer func() { _ = holder.Close() }()

	second := &rosedbDriver{}
	if err := second.Open(dir); err == nil {
		t.Fatal("second Open on the locked directory succeeded, want failure")
	}
}

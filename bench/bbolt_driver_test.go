package bench

import (
	"testing"
	"time"
)

// TestBboltDriverName pins the report name.
func TestBboltDriverName(t *testing.T) {
	if got := (&bboltDriver{}).Name(); got != "bbolt" {
		t.Fatalf("Name() = %q, want %q", got, "bbolt")
	}
}

// TestForReturnsBboltDriver pins the registry's second case: the CLI
// name resolves to a fresh bbolt driver.
func TestForReturnsBboltDriver(t *testing.T) {
	d, err := For("bbolt")
	if err != nil {
		t.Fatalf("For(bbolt): %v", err)
	}
	if d.Name() != "bbolt" {
		t.Fatalf("driver name = %q, want bbolt", d.Name())
	}
}

// TestForUnknownDriverFails pins the registry boundary: an unknown
// name is an error, never a silent default.
func TestForUnknownDriverFails(t *testing.T) {
	for _, name := range []string{"", "rosedb", "elena "} {
		if _, err := For(name); err == nil {
			t.Fatalf("For(%q) succeeded, want error", name)
		}
	}
}

// TestBboltDriverAppendReadRoundtrip drives the full lifecycle against
// a real database file: two keys, two appends each — the second append
// overwriting the first, as bbolt is an in-place update store — then
// reads of both keys resolving to the latest values.
func TestBboltDriverAppendReadRoundtrip(t *testing.T) {
	drv := &bboltDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer drv.Close()

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

// TestBboltDriverReadBeforeAppendFails pins that reads verify against
// the bucket's state: a key never appended reads back nil, which fails
// the run instead of passing silently.
func TestBboltDriverReadBeforeAppendFails(t *testing.T) {
	drv := &bboltDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer drv.Close()

	if err := drv.Do(Op{Kind: OpRead, Key: 42}); err == nil {
		t.Fatal("read of a never-appended key succeeded, want failure")
	}
}

// TestBboltDriverAfterCloseFails pins the lifecycle boundary: once
// closed, operations surface bbolt's closed-database errors rather
// than pretending to succeed.
func TestBboltDriverAfterCloseFails(t *testing.T) {
	drv := &bboltDriver{}
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

// TestBboltDriverOpenLockedDirectory pins the failure path a second
// database on one file takes: bbolt's file lock is its own guard, and
// the driver surfaces it.
func TestBboltDriverOpenLockedDirectory(t *testing.T) {
	dir := t.TempDir()
	holder := &bboltDriver{}
	if err := holder.Open(dir); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer holder.Close()

	second := &bboltDriver{lockTimeout: time.Millisecond} // fail fast instead of waiting out the default
	if err := second.Open(dir); err == nil {
		t.Fatal("second Open on the locked directory succeeded, want failure")
	}
}

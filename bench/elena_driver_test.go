package bench

import (
	"errors"
	"testing"
	"time"

	"elena"
)

// TestElenaDriverName pins the report name.
func TestElenaDriverName(t *testing.T) {
	if got := (&elenaDriver{}).Name(); got != "elena" {
		t.Fatalf("Name() = %q, want %q", got, "elena")
	}
}

// TestForReturnsElenaDriver pins the registry: the name the CLI takes
// resolves to a fresh elena driver.
func TestForReturnsElenaDriver(t *testing.T) {
	d, err := For("elena")
	if err != nil {
		t.Fatalf("For(elena): %v", err)
	}
	if d.Name() != "elena" {
		t.Fatalf("driver name = %q, want elena", d.Name())
	}
}

// TestElenaDriverAppendReadRoundtrip drives the full lifecycle against
// a real store directory: two trajectories, two appends each — the
// second append proving per-key sequencing — then reads of both keys,
// each read resolving to the trajectory's latest event.
func TestElenaDriverAppendReadRoundtrip(t *testing.T) {
	drv := &elenaDriver{}
	dir := t.TempDir()
	if err := drv.Open(dir); err != nil {
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

// TestElenaDriverGroupCommitRoundtrip pins the group-commit tier's
// lifecycle: a driver built by ForTier opens its store under
// WithSyncInterval, appends, reads back, and closes cleanly — the same
// observable behavior as the per-write tier, one durability regime
// below.
func TestElenaDriverGroupCommitRoundtrip(t *testing.T) {
	drv, err := ForTier("elena", SyncGroupCommit, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("ForTier(elena, SyncGroupCommit): %v", err)
	}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = drv.Close() }()

	if err := drv.Do(Op{Kind: OpAppend, Key: 0, Value: []byte("v")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := drv.Do(Op{Kind: OpRead, Key: 0}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// TestElenaDriverReadBeforeAppendFails pins that reads verify against
// the trajectory's state: a key never appended has no latest event, so
// its read fails instead of silently succeeding.
func TestElenaDriverReadBeforeAppendFails(t *testing.T) {
	drv := &elenaDriver{}
	if err := drv.Open(t.TempDir()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = drv.Close() }()

	if err := drv.Do(Op{Kind: OpRead, Key: 42}); err == nil {
		t.Fatal("read of a never-appended key succeeded, want failure")
	}
}

// TestElenaDriverAfterCloseFails pins the lifecycle boundary: once
// closed, operations surface the store's closed errors rather than
// pretending to succeed.
func TestElenaDriverAfterCloseFails(t *testing.T) {
	drv := &elenaDriver{}
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

// TestElenaDriverOpenLockedDirectory pins the failure path a second
// store on one directory takes: the directory lock is the engine's own
// guard, and the driver surfaces it unchanged.
func TestElenaDriverOpenLockedDirectory(t *testing.T) {
	dir := t.TempDir()
	holder, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("elena.Open: %v", err)
	}
	defer func() { _ = holder.Close() }()

	drv := &elenaDriver{}
	err = drv.Open(dir)
	if err == nil {
		t.Fatal("Open on a locked directory succeeded, want failure")
	}
	if !errors.Is(err, elena.ErrDirectoryLocked) {
		t.Fatalf("Open error = %v, want one wrapping ErrDirectoryLocked", err)
	}
}

package engine_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"elena/internal/engine"
)

// Batches provide durable atomicity, not transactions: one Commit =
// one append buffer + one fdatasync + one index publication, so a
// crash leaves the whole batch durable or the whole batch invisible
// (a torn tail is truncated at the next Open). There is no isolation
// and no rollback — see the Batch doc comment. These tests pin that
// contract through the public API.

// A batch accumulates many Puts; a single Commit publishes them all,
// and every key is immediately readable with its exact value.
func TestBatchCommitPublishesAllWrites(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	b := eng.NewBatch()
	want := map[string]string{}
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("batch-key-%03d", i)
		value := fmt.Sprintf("value-%03d-%s", i, strings.Repeat("x", i))
		if err := b.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("batch Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	assertAllReadable(t, eng, want)
}

// Before Commit, no part of a staged batch is observable: not through
// Get, not through Scan. The batch author itself is held to the same
// rule — staging is a private buffer, not a write.
func TestBatchInvisibleBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	if err := eng.Put([]byte("existing"), []byte("v")); err != nil {
		t.Fatalf("seed Put failed: %v", err)
	}

	b := eng.NewBatch()
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("staged-%03d", i)
		if err := b.Put([]byte(key), []byte("v")); err != nil {
			t.Fatalf("batch Put(%q) failed: %v", key, err)
		}
	}

	for i := 0; i < 10; i++ {
		if _, err := eng.Get([]byte(fmt.Sprintf("staged-%03d", i))); !errors.Is(err, engine.ErrNotFound) {
			t.Fatalf("Get before Commit err = %v, want ErrNotFound (batch must be invisible)", err)
		}
	}
	var scanned []string
	if err := eng.Scan(engine.ScanOptions{}, func(key, _ []byte) error {
		scanned = append(scanned, string(key))
		return nil
	}); err != nil {
		t.Fatalf("Scan before Commit failed: %v", err)
	}
	if len(scanned) != 1 || scanned[0] != "existing" {
		t.Errorf("Scan before Commit saw %v, want only [existing]", scanned)
	}
}

// A staged Delete removes an existing key at Commit: the tombstone is
// part of the batch's one durable append, and afterwards the key reads
// exactly like a never-written one. Deleting a missing key stays a
// non-error, matching single-write Delete.
func TestBatchDeleteRemovesExistingKey(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	if err := eng.Put([]byte("doomed"), []byte("v")); err != nil {
		t.Fatalf("seed Put failed: %v", err)
	}

	b := eng.NewBatch()
	if err := b.Delete([]byte("doomed")); err != nil {
		t.Fatalf("batch Delete failed: %v", err)
	}
	if err := b.Delete([]byte("never-existed")); err != nil {
		t.Fatalf("batch Delete of missing key failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if _, err := eng.Get([]byte("doomed")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after batch Delete err = %v, want ErrNotFound", err)
	}
}

// Within one batch, later ops win over earlier ones exactly as if the
// same writes had been issued one by one: the publication loop applies
// staged ops in staging order, so the last op per key decides.
func TestBatchLastOpPerKeyWins(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	b := eng.NewBatch()
	steps := []struct {
		key    string
		value  string // "" means Delete
		delete bool
	}{
		{"k", "first", false},
		{"k", "second", false},
		{"k", "", true},
		{"k", "resurrected", false},
		{"fresh", "only", false},
		{"fresh2", "gone", false},
		{"fresh2", "", true},
	}
	for _, s := range steps {
		if s.delete {
			if err := b.Delete([]byte(s.key)); err != nil {
				t.Fatalf("batch Delete(%q) failed: %v", s.key, err)
			}
			continue
		}
		if err := b.Put([]byte(s.key), []byte(s.value)); err != nil {
			t.Fatalf("batch Put(%q) failed: %v", s.key, err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	want := map[string]string{"k": "resurrected", "fresh": "only"}
	assertAllReadable(t, eng, want)
	if _, err := eng.Get([]byte("fresh2")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(fresh2) after staged put+delete err = %v, want ErrNotFound", err)
	}
	if got := eng.Stats().LiveEntries; got != uint64(len(want)) {
		t.Errorf("LiveEntries after batch = %d, want %d", got, len(want))
	}
}

// A staged PutWithTTL behaves like its single-write counterpart: the
// entry is readable while live and becomes invisible once its deadline
// passes — the expiry travels in the entry header inside the batch's
// durable append, so it survives Close/Open.
func TestBatchPutWithTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)

	b := eng.NewBatch()
	if err := b.PutWithTTL([]byte("expiring"), []byte("v"), shortTTL); err != nil {
		t.Fatalf("batch PutWithTTL failed: %v", err)
	}
	if err := b.Put([]byte("plain"), []byte("v")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	assertAllReadable(t, eng, map[string]string{"expiring": "v", "plain": "v"})
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	time.Sleep(expiryWait)
	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)
	if _, err := reopened.Get([]byte("expiring")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(expiring) after batch TTL expiry and reopen err = %v, want ErrNotFound", err)
	}
	if _, err := reopened.Get([]byte("plain")); err != nil {
		t.Errorf("Get(plain) after reopen err = %v, want nil", err)
	}
}

// Staging rejects what single writes reject, at staging time: oversized
// keys and values, and TTLs that carry no honest meaning. A rejected op
// leaves nothing behind — not in the batch, not on disk.
func TestBatchStagingRejectsInvalidOps(t *testing.T) {
	tests := []struct {
		name    string
		stage   func(b *engine.Batch) error
		wantErr error
	}{
		{
			name:    "oversized key",
			stage:   func(b *engine.Batch) error { return b.Put(bytesOfLength(1024+1), []byte("v")) },
			wantErr: engine.ErrKeyTooLarge,
		},
		{
			name:    "oversized value",
			stage:   func(b *engine.Batch) error { return b.Put([]byte("k"), bytesOfLength(4<<20+1)) },
			wantErr: engine.ErrValueTooLarge,
		},
		{
			name:    "oversized delete key",
			stage:   func(b *engine.Batch) error { return b.Delete(bytesOfLength(1024 + 1)) },
			wantErr: engine.ErrKeyTooLarge,
		},
		{
			name:    "zero ttl",
			stage:   func(b *engine.Batch) error { return b.PutWithTTL([]byte("k"), []byte("v"), 0) },
			wantErr: engine.ErrInvalidTTL,
		},
		{
			name:    "negative ttl",
			stage:   func(b *engine.Batch) error { return b.PutWithTTL([]byte("k"), []byte("v"), -time.Second) },
			wantErr: engine.ErrInvalidTTL,
		},
		{
			name:    "unrepresentable ttl",
			stage:   func(b *engine.Batch) error { return b.PutWithTTL([]byte("k"), []byte("v"), math.MaxInt64) },
			wantErr: engine.ErrInvalidTTL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := openEngine(t, t.TempDir())
			defer closeEngine(t, eng)

			b := eng.NewBatch()
			if err := b.Put([]byte("keep"), []byte("v")); err != nil {
				t.Fatalf("seed batch Put failed: %v", err)
			}
			if err := tt.stage(b); !errors.Is(err, tt.wantErr) {
				t.Fatalf("stage err = %v, want %v", err, tt.wantErr)
			}
			// The batch still holds only the valid op before it.
			if err := b.Commit(); err != nil {
				t.Fatalf("Commit after rejected stage failed: %v", err)
			}
			if _, err := eng.Get([]byte("keep")); err != nil {
				t.Errorf("Get(keep) after rejected stage and Commit err = %v, want nil", err)
			}
		})
	}
}

// Staging and Commit after Close must fail with ErrClosed, matching
// every other public operation.
func TestBatchAfterClose(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	b := eng.NewBatch()
	if err := b.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := b.Put([]byte("k2"), []byte("v")); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("stage after Close err = %v, want ErrClosed", err)
	}
	if err := b.Commit(); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Commit after Close err = %v, want ErrClosed", err)
	}
}

// Staging copies key and value: the batch must not alias the caller's
// slices, or a caller mutating its buffers between staging and Commit
// would silently change what gets committed.
func TestBatchCopiesKeyAndValueAtStaging(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	key := []byte("mutating-key")
	value := []byte("original")
	b := eng.NewBatch()
	if err := b.Put(key, value); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	key[0] = 'M'
	value[0] = 'X'
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	got, err := eng.Get([]byte("mutating-key"))
	if err != nil {
		t.Fatalf("Get(mutating-key) err = %v, want nil (the staged copy, not the mutated slice)", err)
	}
	if string(got) != "original" {
		t.Errorf("Get after caller mutated its slices = %q, want %q", got, "original")
	}
}

// A committed batch must survive Close/Open with every key and exact
// value — the batch's single append is ordinary durable engine state,
// so replay reconstructs it like any other write.
func TestBatchSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)

	want := map[string]string{}
	b := eng.NewBatch()
	for i := 0; i < 15; i++ {
		key := fmt.Sprintf("reopen-%03d", i)
		value := fmt.Sprintf("value-%03d-%s", i, strings.Repeat("y", 3*i))
		if err := b.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("batch Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}
	if err := b.Delete([]byte("reopen-003")); err != nil {
		t.Fatalf("batch Delete failed: %v", err)
	}
	delete(want, "reopen-003")
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
	if _, err := reopened.Get([]byte("reopen-003")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(batch-deleted key) after reopen err = %v, want ErrNotFound", err)
	}
}

// Batches mix with single writes on the same writer in strict call
// order: every write — single or batched — takes effect exactly where
// it was issued, and the last write per key wins. This is same-writer
// serialization: call order is commit order.
func TestBatchOrderingWithSingleWrites(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)

	key := []byte("shared")
	step := func(put func() error, want string) {
		t.Helper()
		if err := put(); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		got, err := eng.Get(key)
		if err != nil || string(got) != want {
			t.Fatalf("after step wanting %q: Get = %q, %v", want, got, err)
		}
	}

	// Each kind of write observes the one before it and is observed by
	// the one after it.
	step(func() error { return eng.Put(key, []byte("single-1")) }, "single-1")

	b := eng.NewBatch()
	if err := b.Put(key, []byte("batch-2")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	got, _ := eng.Get(key)
	if string(got) != "batch-2" {
		t.Fatalf("after batch Commit: Get(shared) = %q, want %q", got, "batch-2")
	}

	step(func() error { return eng.Put(key, []byte("single-3")) }, "single-3")

	b2 := eng.NewBatch()
	if err := b2.Delete(key); err != nil {
		t.Fatalf("batch Delete failed: %v", err)
	}
	if err := b2.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if _, err := eng.Get(key); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("after batch Delete: Get err = %v, want ErrNotFound", err)
	}

	// A single write that runs while a batch sits staged lands first,
	// then the batch overwrites it: staging order across mechanisms is
	// still call order for the single writer.
	b3 := eng.NewBatch()
	if err := b3.Put(key, []byte("batch-5")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	step(func() error { return eng.Put(key, []byte("single-4")) }, "single-4")
	if err := b3.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	got, err := eng.Get(key)
	if err != nil || string(got) != "batch-5" {
		t.Fatalf("after staged-then-committed batch: Get(shared) = %q, %v; want %q", got, err, "batch-5")
	}
}

// Committing an empty batch is a no-op (no bytes flushed), and a batch
// is reusable after Commit: the next staged writes form a fresh batch.
func TestBatchEmptyCommitAndReuse(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	b := eng.NewBatch()
	before := eng.Stats().BytesFlushed
	if err := b.Commit(); err != nil {
		t.Fatalf("empty Commit failed: %v", err)
	}
	if after := eng.Stats().BytesFlushed; after != before {
		t.Errorf("empty Commit flushed bytes: %d -> %d, want no change", before, after)
	}

	// First round.
	if err := b.Put([]byte("round-1"), []byte("v1")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("first Commit failed: %v", err)
	}
	// Second round on the same batch object: the first round must not
	// leak into it.
	if err := b.Put([]byte("round-2"), []byte("v2")); err != nil {
		t.Fatalf("batch Put failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("second Commit failed: %v", err)
	}

	assertAllReadable(t, eng, map[string]string{"round-1": "v1", "round-2": "v2"})
	if got := eng.Stats().LiveEntries; got != 2 {
		t.Errorf("LiveEntries after two reused-batch rounds = %d, want 2", got)
	}
}

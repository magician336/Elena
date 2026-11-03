package engine_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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

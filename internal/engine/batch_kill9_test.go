//go:build unix

package engine_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/magician336/elena/internal/engine"
)

// The kill -9 proof of batch durable atomicity: a child process commits
// batches through the public API, the parent kills it mid-run, and the
// reopened directory must hold no half batch — every batch is either
// fully visible with exact values or fully invisible. Batched writes
// sit in the same always-sync regime as single puts: a Commit's single
// append plus single fdatasync is over before its ack lines appear, so
// observed acks imply whole durable batches.

// A child commits batches of eight large puts; the parent kills it
// after the 25th ack. After reopen: every batch of the deterministic
// dataset is entirely present or entirely absent, no acknowledged key
// is lost, and every present value is byte-exact. The large values
// stretch each Commit's append+sync window so the kill can land inside
// a batch commit — whichever way it lands, a torn tail truncates to a
// whole-batch boundary.
func TestKill9MidBatchNoPartialBatch(t *testing.T) {
	dir := t.TempDir()
	const batchSize = 8
	spec := WriterSpec{Dir: dir, Writes: 24 * batchSize, ValueSize: 32 << 10, Seed: 42, BatchSize: batchSize}

	acked := KillWriterAfterAcks(t, spec, 25)
	assertNoHalfBatches(t, dir, spec, acked, batchSize)
}

// The same all-or-nothing verdict with real rotation in play: the
// child's engine rotates its active file while batches keep coming, so
// a killed commit may straddle a rotation moment. Rotation only ever
// happens after a batch is durable, so batch atomicity survives it.
func TestKill9MidBatchAcrossRotationNoPartialBatch(t *testing.T) {
	dir := t.TempDir()
	const batchSize = 5
	spec := WriterSpec{Dir: dir, Writes: 40 * batchSize, ValueSize: 96, Seed: 43, BatchSize: batchSize, RotateEvery: 12}

	acked := KillWriterAfterAcks(t, spec, 60)
	if got := countDataFilesIn(t, dir); got < 3 {
		t.Fatalf("fixture sanity: want at least 3 data files from real rotations at kill time, got %d", got)
	}
	assertNoHalfBatches(t, dir, spec, acked, batchSize)
}

// assertNoHalfBatches reopens dir and verifies the batch-atomicity
// verdict over the whole dataset: batch i (dataset[i*size:(i+1)*size])
// must be uniformly visible or invisible, values byte-exact, and every
// acknowledged write present. It also proves the directory is still
// writable and resolvable across another reopen.
func assertNoHalfBatches(t *testing.T, dir string, spec WriterSpec, acked []AckedWrite, batchSize int) {
	t.Helper()

	eng := openEngine(t, dir)
	dataset := WriterDataset(spec)

	ackedSet := make(map[string]bool, len(acked))
	for _, w := range acked {
		ackedSet[w.Key] = true
	}

	for start := 0; start < len(dataset); start += batchSize {
		end := min(start+batchSize, len(dataset))
		visible := 0
		for _, w := range dataset[start:end] {
			got, err := eng.Get([]byte(w.Key))
			switch {
			case err == nil:
				if !bytes.Equal(got, w.Value) {
					t.Fatalf("Get(%q) after mid-batch kill = %d bytes, want the exact dataset value", w.Key, len(got))
				}
				visible++
			case errors.Is(err, engine.ErrNotFound):
			default:
				t.Fatalf("Get(%q) after mid-batch kill err = %v, want hit or ErrNotFound", w.Key, err)
			}
			if ackedSet[w.Key] && err != nil {
				t.Fatalf("acknowledged batch write %q lost after kill -9: %v", w.Key, err)
			}
		}
		if visible != 0 && visible != end-start {
			t.Fatalf("half batch after kill -9: batch %d..%d has %d of %d keys visible — batches must be all-or-nothing",
				start, end, visible, end-start)
		}
	}

	// The recovered directory must accept new writes that survive yet
	// another reopen.
	if err := eng.Put([]byte("post-kill"), []byte("still-writable")); err != nil {
		t.Fatalf("Put after mid-batch kill and reopen failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close after mid-batch kill failed: %v", err)
	}
	final := openEngine(t, dir)
	defer closeEngine(t, final)
	if got, err := final.Get([]byte("post-kill")); err != nil || string(got) != "still-writable" {
		t.Errorf("Get(post-kill) after second reopen = %q, %v; want \"still-writable\", nil", got, err)
	}
}

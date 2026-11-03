package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"elena/internal/engine"
)

// Merge must reclaim dead data while preserving every live entry: the
// live set and its bytes survive byte-for-byte, dead entries drop to
// zero, the old data and hint files leave the directory, and the
// rewrite itself shows up in the flushed-byte counter (merges are part
// of the write-amplification story).
func TestMergeReclaimsDeadDataAndKeepsLive(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)

	want := writeRotatedFixture(t, eng, 40)
	// Churn: overwrite half, delete a few — the dead share grows.
	for i := 0; i < 20; i++ {
		key := keyOf(i)
		if err := eng.Put([]byte(key), []byte(want[key]+"-new")); err != nil {
			t.Fatalf("overwrite Put(%q) failed: %v", key, err)
		}
		want[key] = want[key] + "-new"
	}
	for i := 20; i < 25; i++ {
		key := keyOf(i)
		if err := eng.Delete([]byte(key)); err != nil {
			t.Fatalf("Delete(%q) failed: %v", key, err)
		}
		delete(want, key)
	}

	before := eng.Stats()
	if before.DeadEntries == 0 || before.DeadRatio <= 0 {
		t.Fatalf("before merge: DeadEntries = %d, DeadRatio = %v, want churn to produce dead data", before.DeadEntries, before.DeadRatio)
	}

	if err := eng.Merge(); err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}

	after := eng.Stats()
	if after.LiveEntries != uint64(len(want)) {
		t.Errorf("after merge: LiveEntries = %d, want %d (unchanged live set)", after.LiveEntries, len(want))
	}
	if after.DeadEntries != 0 {
		t.Errorf("after merge: DeadEntries = %d, want 0 (all dead entries reclaimed)", after.DeadEntries)
	}
	if after.DeadRatio != 0 {
		t.Errorf("after merge: DeadRatio = %v, want 0", after.DeadRatio)
	}
	if after.LiveBytes != before.LiveBytes {
		t.Errorf("after merge: LiveBytes = %d, want %d (live bytes unchanged)", after.LiveBytes, before.LiveBytes)
	}
	if after.DataBytes >= before.DataBytes {
		t.Errorf("after merge: DataBytes = %d, want < %d (dead bytes left the disk)", after.DataBytes, before.DataBytes)
	}
	if after.BytesFlushed <= before.BytesFlushed {
		t.Errorf("after merge: BytesFlushed = %d, want > %d (the rewrite itself is flushed disk traffic)", after.BytesFlushed, before.BytesFlushed)
	}
	if after.OpsMerge != 1 {
		t.Errorf("OpsMerge = %d, want 1", after.OpsMerge)
	}

	assertAllReadable(t, eng, want)

	// The directory keeps only the merged output plus the fresh active
	// file, each with at most one hint.
	if got := countDataFilesIn(t, dir); got != 2 {
		t.Errorf("data file count after merge = %d, want 2 (merged output + new active)", got)
	}
	if got := countExtIn(t, dir, ".hint"); got != 1 {
		t.Errorf("hint file count after merge = %d, want 1 (for the merged output; the active file has none yet)", got)
	}
}

// Merge on a fresh engine is a no-op that succeeds: nothing to
// reclaim, no error, and the observation port still counts the call.
func TestMergeOnEmptyEngineIsNoop(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	if err := eng.Merge(); err != nil {
		t.Fatalf("Merge() on empty engine failed: %v", err)
	}
	if s := eng.Stats(); s.LiveEntries != 0 || s.DeadEntries != 0 {
		t.Errorf("after merge on empty engine: live = %d, dead = %d, want 0/0", s.LiveEntries, s.DeadEntries)
	}
}

// Merge after Close must fail with ErrClosed like every other engine
// operation.
func TestMergeOnClosedEngineFails(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	mustPut(t, eng, "k", "v")
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := eng.Merge(); err != engine.ErrClosed {
		t.Errorf("Merge() after Close err = %v, want ErrClosed", err)
	}
}

// Two merges in a row must both succeed and keep the data intact —
// the second one runs against the first one's output.
func TestMergeTwiceKeepsData(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)

	want := writeRotatedFixture(t, eng, 20)
	if err := eng.Merge(); err != nil {
		t.Fatalf("first Merge() failed: %v", err)
	}
	if err := eng.Merge(); err != nil {
		t.Fatalf("second Merge() failed: %v", err)
	}
	assertAllReadable(t, eng, want)
	if s := eng.Stats(); s.DeadEntries != 0 {
		t.Errorf("DeadEntries after second merge = %d, want 0", s.DeadEntries)
	}
}

// keyOf returns the fixture key of index i, matching
// writeRotatedFixture's key pattern.
func keyOf(i int) string {
	return fmt.Sprintf("key%03d", i)
}

// countExtIn returns how many files in dir end with ext.
func countExtIn(t *testing.T, dir, ext string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ext {
			n++
		}
	}
	return n
}

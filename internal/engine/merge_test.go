package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/magician336/elena/internal/engine"
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

// Unexpired TTL entries must survive a merge byte-for-byte — expiry
// header included — so the countdown keeps running across the merge
// and a reopen, and the entry disappears exactly when its deadline
// says so.
func TestMergeKeepsUnexpiredTTLUntilDeadline(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	mustPut(t, eng, "plain", "v")
	mustPutWithTTL(t, eng, "ephemeral", "v", 500*time.Millisecond)

	if err := eng.Merge(); err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}
	if got, err := eng.Get([]byte("ephemeral")); err != nil || string(got) != "v" {
		t.Fatalf("Get(ephemeral) after merge = %q, %v, want the live TTL entry", got, err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	time.Sleep(600 * time.Millisecond) // let the deadline pass across Close/Open
	reopened := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, reopened)
	if _, err := reopened.Get([]byte("ephemeral")); err != engine.ErrNotFound {
		t.Errorf("Get(ephemeral) after deadline = %v, want ErrNotFound (expiry header survived the merge)", err)
	}
	if got, err := reopened.Get([]byte("plain")); err != nil || string(got) != "v" {
		t.Errorf("Get(plain) after merge+reopen = %q, %v, want the untouched value", got, err)
	}
}

// Expired entries are physically reclaimed by merge: they leave the
// index, their bytes leave the disk (observable as a DataBytes drop),
// and the dead accounting resets to zero.
func TestMergePhysicallyReclaimsExpiredEntries(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "keep-1", "value-1")
	mustPut(t, eng, "keep-2", "value-2")
	mustPutWithTTL(t, eng, "gone-1", "value-1", 50*time.Millisecond)
	mustPutWithTTL(t, eng, "gone-2", "value-2", 50*time.Millisecond)

	time.Sleep(80 * time.Millisecond) // both TTL entries expire
	before := eng.Stats()
	if _, err := eng.Get([]byte("gone-1")); err != engine.ErrNotFound {
		t.Fatalf("Get(gone-1) after expiry err = %v, want ErrNotFound", err)
	}

	if err := eng.Merge(); err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}

	after := eng.Stats()
	if after.LiveEntries != 2 {
		t.Errorf("LiveEntries after merge = %d, want 2 (expired keys left the index)", after.LiveEntries)
	}
	if after.DataBytes >= before.DataBytes {
		t.Errorf("DataBytes after merge = %d, want < %d (expired bytes physically reclaimed)", after.DataBytes, before.DataBytes)
	}
	if after.DeadEntries != 0 || after.DeadRatio != 0 {
		t.Errorf("dead accounting after merge: entries = %d, ratio = %v, want 0/0", after.DeadEntries, after.DeadRatio)
	}
	if _, err := eng.Get([]byte("gone-1")); err != engine.ErrNotFound {
		t.Errorf("Get(gone-1) after merge err = %v, want ErrNotFound", err)
	}
	if got, err := eng.Get([]byte("keep-1")); err != nil || string(got) != "value-1" {
		t.Errorf("Get(keep-1) after merge = %q, %v, want live value", got, err)
	}
}

// After a merge, Open must rebuild the index through the merged
// output's hint file and serve every live entry — the hint written by
// the merge is a first-class startup path, not an afterthought.
func TestMergeOutputHintFastPathReopensCleanly(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	want := writeRotatedFixture(t, eng, 30)
	for i := 0; i < 10; i++ { // churn so the merge has real work
		key := keyOf(i)
		if err := eng.Delete([]byte(key)); err != nil {
			t.Fatalf("Delete(%q) failed: %v", key, err)
		}
		delete(want, key)
	}
	if err := eng.Merge(); err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}
	if got := countExtIn(t, dir, ".hint"); got != 1 {
		t.Fatalf("hint files after merge = %d, want 1 (the merged output's)", got)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
	if s := reopened.Stats(); s.LiveEntries != uint64(len(want)) || s.DeadEntries != 0 {
		t.Errorf("after reopen via hint: live = %d, dead = %d, want %d/0", s.LiveEntries, s.DeadEntries, len(want))
	}
}

// A crashed hint write leaves a ".hint.tmp" residue behind. Open must
// remove the residue (a fresh engine would otherwise carry it
// forever), and a merge must clean the residue of the files it
// reclaims.
func TestHintTmpResidueIsCleaned(t *testing.T) {
	t.Run("open removes residue", func(t *testing.T) {
		dir := t.TempDir()
		eng := openEngineWithOptions(t, dir, engine.Options{})
		mustPut(t, eng, "k", "v")
		if err := eng.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
		residue := filepath.Join(dir, "0000000007.hint.tmp")
		if err := os.WriteFile(residue, []byte("half-written"), 0o644); err != nil {
			t.Fatalf("create residue: %v", err)
		}

		reopened := openEngineWithOptions(t, dir, engine.Options{})
		defer closeEngine(t, reopened)
		if _, err := os.Stat(residue); !os.IsNotExist(err) {
			t.Errorf("hint tmp residue still present after Open: stat err = %v, want IsNotExist", err)
		}
		if got, err := reopened.Get([]byte("k")); err != nil || string(got) != "v" {
			t.Errorf("Get(k) after residue cleanup = %q, %v, want live value", got, err)
		}
	})

	t.Run("merge removes residue of merged files", func(t *testing.T) {
		dir := t.TempDir()
		eng := openEngineWithOptions(t, dir, engine.Options{})
		defer closeEngine(t, eng)
		mustPut(t, eng, "k", "v")
		// Residue of the active data file's ID (the file merge will
		// rotate away and reclaim).
		residue := filepath.Join(dir, "0000000001.hint.tmp")
		if err := os.WriteFile(residue, []byte("half-written"), 0o644); err != nil {
			t.Fatalf("create residue: %v", err)
		}
		if err := eng.Merge(); err != nil {
			t.Fatalf("Merge() failed: %v", err)
		}
		if _, err := os.Stat(residue); !os.IsNotExist(err) {
			t.Errorf("hint tmp residue still present after Merge: stat err = %v, want IsNotExist", err)
		}
	})
}

// Options.MergeDeadRatio makes the write path merge by itself: once a
// successful write pushes the dead share past the threshold, a merge
// runs before that call returns — no explicit Merge() anywhere. Below
// the threshold nothing fires; with the default (disabled) option
// heavy churn never fires one either.
func TestAutoMergeTriggersWhenDeadRatioExceedsThreshold(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{MergeDeadRatio: 0.5})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k1", "v1")
	mustPut(t, eng, "k2", "v2")
	mustPut(t, eng, "k1", "v1-longer") // dead share 1/3 of the bytes: below 0.5
	if s := eng.Stats(); s.OpsMerge != 0 {
		t.Fatalf("OpsMerge = %d below threshold, want 0 (no auto merge yet)", s.OpsMerge)
	}

	if err := eng.Delete([]byte("k1")); err != nil {
		t.Fatalf("Delete(k1) failed: %v", err)
	}
	s := eng.Stats()
	if s.OpsMerge == 0 {
		t.Fatal("OpsMerge = 0 after a write crossed the threshold, want the write path to have merged automatically")
	}
	if s.DeadEntries != 0 {
		t.Errorf("DeadEntries = %d after auto merge, want 0", s.DeadEntries)
	}
	if got, err := eng.Get([]byte("k2")); err != nil || string(got) != "v2" {
		t.Errorf("Get(k2) after auto merge = %q, %v, want v2", got, err)
	}
	if _, err := eng.Get([]byte("k1")); err != engine.ErrNotFound {
		t.Errorf("Get(k1) after auto merge err = %v, want ErrNotFound", err)
	}
}

// Without an explicit threshold the engine must never merge on its
// own, however much dead data churn produces.
func TestAutoMergeDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	for round := 0; round < 10; round++ {
		mustPut(t, eng, "k", "value") // fresh or overwrite
		if err := eng.Delete([]byte("k")); err != nil {
			t.Fatalf("Delete(k) failed: %v", err)
		}
	}
	s := eng.Stats()
	if s.OpsMerge != 0 {
		t.Errorf("OpsMerge = %d with default options, want 0 (auto merge is opt-in)", s.OpsMerge)
	}
	if s.DeadEntries == 0 {
		t.Error("DeadEntries = 0 after churn, want dead data to accumulate while merging is disabled")
	}
}

// A threshold above 1 is a configuration error (the dead share can
// never exceed 1, so nothing could ever strictly pass it), while
// values close to the boundary are accepted and still trigger when
// the dead share reaches its maximum.
func TestOpenValidatesMergeDeadRatio(t *testing.T) {
	if eng, err := engine.Open(t.TempDir(), engine.Options{MergeDeadRatio: 1.5}); err == nil {
		closeEngine(t, eng)
		t.Fatal("Open with MergeDeadRatio 1.5 succeeded, want a configuration error")
	}
	eng, err := engine.Open(t.TempDir(), engine.Options{MergeDeadRatio: 0.9})
	if err != nil {
		t.Fatalf("Open with MergeDeadRatio 0.9 failed: %v", err)
	}
	defer closeEngine(t, eng)
	mustPut(t, eng, "k", "v")
	if err := eng.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete(k) failed: %v", err)
	}
	if s := eng.Stats(); s.OpsMerge == 0 {
		t.Error("OpsMerge = 0 with threshold 0.9 and a fully dead disk, want an auto merge")
	}
}

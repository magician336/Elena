package engine_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"elena/internal/engine"
)

// hintedState is the expected outcome of the hint-reopen fixture: the
// visible key/value map plus every key the fixture ever wrote (visible
// or not), so agreement tests can probe both.
type hintedState struct {
	visible  map[string]string
	allKeys  []string
	dataSize map[uint32]int64 // data file ID -> size at fixture end
}

// writeHintedFixture builds a directory whose reopen exercises the
// interesting index semantics across rotated files: overwrites that
// cross file boundaries, a tombstone that stays hidden, a deleted key
// that is resurrected, and plain surviving entries. It closes the
// engine (so every immutable file has a hint and the final active file
// does too) and returns the expected state.
func writeHintedFixture(t *testing.T, dir string) hintedState {
	t.Helper()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	visible := map[string]string{}
	var allKeys []string
	put := func(key, value string) {
		t.Helper()
		if err := eng.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
		visible[key] = value
		allKeys = append(allKeys, key)
	}
	del := func(key string) {
		t.Helper()
		if err := eng.Delete([]byte(key)); err != nil {
			t.Fatalf("Delete(%q) failed: %v", key, err)
		}
		delete(visible, key)
	}

	// Enough plain entries to rotate across several files (the small
	// threshold holds 15 of these 43-byte entries per file, so the
	// follow-up writes below land in a later file than key000's
	// original entry: the overwrite crosses a file boundary).
	for i := 0; i < 34; i++ {
		put(fmt.Sprintf("key%03d", i), fmt.Sprintf("value%03d", i))
	}
	put("key000", "overwritten-across-files") // wins from a later file
	del("key005")                             // tombstone stays hidden
	del("key003")
	put("key003", "resurrected") // latest write wins again

	if err := eng.Close(); err != nil {
		t.Fatalf("Close after fixture failed: %v", err)
	}

	logIDs := fileIDsWithExt(t, dir, ".log")
	if len(logIDs) < 3 {
		t.Fatalf("fixture sanity: want >= 3 data files, got %v", logIDs)
	}
	dataSize := map[uint32]int64{}
	for _, id := range logIDs {
		dataSize[id] = fileSizeOf(t, dataFilePathFor(t, dir, id))
	}
	return hintedState{visible: visible, allKeys: allKeys, dataSize: dataSize}
}

// assertStateMatches asserts the engine's visible state matches st.
func assertStateMatches(t *testing.T, eng *engine.Engine, st hintedState, label string) {
	t.Helper()
	sort.Strings(st.allKeys)
	for _, key := range st.allKeys {
		got, err := eng.Get([]byte(key))
		want, visible := st.visible[key]
		switch {
		case visible && err != nil:
			t.Fatalf("%s: Get(%q) err = %v, want %q", label, key, err, want)
		case visible && string(got) != want:
			t.Errorf("%s: Get(%q) = %q, want %q", label, key, got, want)
		case !visible && !errors.Is(err, engine.ErrNotFound):
			t.Errorf("%s: Get(%q) = %q, %v; want ErrNotFound", label, key, got, err)
		}
	}
}

// Deleting a data file's hint (a crash can leave files without one)
// must not stop Open or lose data: the engine falls back to scanning
// that file and rebuilds exactly the same index.
func TestHintMissingReopenStillComplete(t *testing.T) {
	dir := t.TempDir()
	st := writeHintedFixture(t, dir)

	logIDs := fileIDsWithExt(t, dir, ".log")
	victim := logIDs[0] // an immutable file: drop its hint
	if err := os.Remove(hintPathFor(t, dir, victim)); err != nil {
		t.Fatalf("remove hint of file %d: %v", victim, err)
	}

	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)
	assertStateMatches(t, eng, st, "reopen without hint of file "+fmt.Sprint(victim))
}

// The fallback path is not a stub: without a hint, Open really scans
// the data file, so a corrupted entry tail that a scan must truncate
// does get truncated. (With a valid hint the same corruption is left
// untouched — that pairing pins which path Open took.)
func TestHintlessReopenScansAndTruncatesCorruptedTail(t *testing.T) {
	dir := t.TempDir()
	writeHintedFixture(t, dir)

	logIDs := fileIDsWithExt(t, dir, ".log")
	victim := logIDs[0]
	if err := os.Remove(hintPathFor(t, dir, victim)); err != nil {
		t.Fatalf("remove hint of file %d: %v", victim, err)
	}
	victimPath := dataFilePathFor(t, dir, victim)
	flipLastByte(t, victimPath)
	before := fileSizeOf(t, victimPath)

	eng := openEngineWithOptions(t, dir, smallFileOptions())
	if got := fileSizeOf(t, victimPath); got >= before {
		t.Fatalf("corrupted tail in hintless file %d was not truncated (size %d, before %d); the fallback must scan the data file", victim, got, before)
	}

	// The engine stays fully usable after the truncating reopen.
	if err := eng.Put([]byte("post-fallback"), []byte("ok")); err != nil {
		t.Fatalf("Put after fallback reopen failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close after fallback reopen failed: %v", err)
	}
}

// copyDir copies every regular file from src to dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read dir %q: %v", src, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %q: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatalf("write %q: %v", e.Name(), err)
		}
	}
}

// Reopening with hints and reopening by scanning the data files must
// build the same index: for every key the fixture ever written, both
// engines agree on existence and on the exact value. Overwrites,
// tombstones, and resurrections included.
func TestHintedAndHintlessReopenBuildSameIndex(t *testing.T) {
	dir := t.TempDir()
	st := writeHintedFixture(t, dir)

	scanned := filepath.Join(t.TempDir(), "no-hints")
	copyDir(t, dir, scanned)
	for _, id := range fileIDsWithExt(t, scanned, ".hint") {
		if err := os.Remove(hintPathFor(t, scanned, id)); err != nil {
			t.Fatalf("remove hint %d: %v", id, err)
		}
	}

	hintedEng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, hintedEng)
	scannedEng := openEngineWithOptions(t, scanned, engine.Options{})
	defer closeEngine(t, scannedEng)

	sort.Strings(st.allKeys)
	for _, key := range st.allKeys {
		fromHint, hintErr := hintedEng.Get([]byte(key))
		fromScan, scanErr := scannedEng.Get([]byte(key))

		switch {
		case hintErr == nil && scanErr == nil:
			if !bytes.Equal(fromHint, fromScan) {
				t.Errorf("Get(%q): hinted = %q but scanned = %q", key, fromHint, fromScan)
			}
		case hintErr == nil || scanErr == nil:
			t.Errorf("Get(%q): hinted err = %v, scanned err = %v; indexes disagree", key, hintErr, scanErr)
		case !errors.Is(hintErr, engine.ErrNotFound) || !errors.Is(scanErr, engine.ErrNotFound):
			t.Errorf("Get(%q): hinted err = %v, scanned err = %v; want both ErrNotFound", key, hintErr, scanErr)
		}
	}
}

package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"elena/internal/engine"
)

// openEngineWithOptions opens dir with explicit Options.
func openEngineWithOptions(t *testing.T, dir string, opts engine.Options) *engine.Engine {
	t.Helper()
	eng, err := engine.Open(dir, opts)
	if err != nil {
		t.Fatalf("Open(%q, %+v) failed: %v", dir, opts, err)
	}
	return eng
}

// rotateThresholdEntries is the per-entry encoded footprint of the
// fixture dataset below: entryHeaderSize (28) + 7-byte key + 120-byte
// value, so a MaxDataFileSize of rotateThresholdEntries * N rotates
// after roughly every N puts.
const rotateFixtureValueSize = 120

// smallFileOptions returns Options whose data-file threshold holds only
// a few fixture entries, so rotation behavior is exercised quickly.
func smallFileOptions() engine.Options {
	return engine.Options{MaxDataFileSize: 4 * (28 + 7 + rotateFixtureValueSize)}
}

// writeRotatedFixture puts keys key000..keyN-1 through the public API
// and returns the expected key/value map.
func writeRotatedFixture(t *testing.T, eng *engine.Engine, n int) map[string]string {
	t.Helper()
	want := map[string]string{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("key%03d", i)
		value := fmt.Sprintf("value%03d", i) + strings.Repeat("x", rotateFixtureValueSize)
		if err := eng.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}
	return want
}

// assertAllReadable asserts every entry of want reads back with its
// exact value through the public API.
func assertAllReadable(t *testing.T, eng *engine.Engine, want map[string]string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatal("fixture map is empty")
	}
	for key, value := range want {
		got, err := eng.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", key, err)
		}
		if string(got) != value {
			t.Errorf("Get(%q) = %d bytes, want the exact %d-byte fixture value", key, len(got), len(value))
		}
	}
}

// When the active file grows past Options.MaxDataFileSize, writing must
// rotate: the current file stays on disk as an immutable file (the
// single-file era ends), a fresh active file with a higher monotonic
// ID takes over, and every written key remains readable across the
// rotation boundary. A reopen afterwards replays all the files.
func TestPutRotatesActiveFileBySizeThreshold(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	want := writeRotatedFixture(t, eng, 40)
	if got := countDataFilesIn(t, dir); got < 5 {
		t.Fatalf("data file count after 40 puts across the threshold = %d, want >= 5 (rotation did not split files)", got)
	}
	assertAllReadable(t, eng, want)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close after rotation failed: %v", err)
	}

	reopened := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
}

// countDataFilesIn returns how many engine data files dir currently
// holds (fixture sanity; assertions on data itself still go through
// the public API).
func countDataFilesIn(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".log" {
			n++
		}
	}
	return n
}

// fileIDsWithExt returns the sorted numeric file IDs of the files in
// dir whose name ends with ext (e.g. ".log" or ".hint").
func fileIDsWithExt(t *testing.T, dir, ext string) []uint32 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	var ids []uint32
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ext {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSuffix(name, ext), 10, 32)
		if err != nil {
			t.Fatalf("parse file ID from %q: %v", name, err)
		}
		ids = append(ids, uint32(id))
	}
	slices.Sort(ids)
	return ids
}

// hintFilesFor returns the subset of data-file IDs that have a hint
// file in dir.
func hintFilesFor(t *testing.T, dir string) map[uint32]bool {
	t.Helper()
	withHint := map[uint32]bool{}
	for _, id := range fileIDsWithExt(t, dir, ".hint") {
		withHint[id] = true
	}
	return withHint
}

// Rotation must leave a hint file behind for every immutable data
// file: the rotation point is when the file's contents stop changing,
// so a compact key-to-position map can be written durably alongside
// it. The highest-ID file is the active one (its hint arrives at
// Close), so only the lower IDs are asserted here.
func TestRotationWritesHintForEachImmutableFile(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	writeRotatedFixture(t, eng, 20)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close after rotation failed: %v", err)
	}

	logIDs := fileIDsWithExt(t, dir, ".log")
	if len(logIDs) < 3 {
		t.Fatalf("fixture sanity: want >= 3 data files from rotation, got %v", logIDs)
	}
	withHint := hintFilesFor(t, dir)
	for _, id := range logIDs[:len(logIDs)-1] {
		if !withHint[id] {
			t.Errorf("immutable data file %d has no hint file after rotation", id)
		}
	}
}

// Close must write a hint for the active file too, so a clean
// shutdown also leaves a fast-start map behind. Without any rotation
// there is exactly one data file and it must get its hint.
func TestCloseWritesHintForActiveFile(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{}) // default: no rotation

	want := writeRotatedFixture(t, eng, 3)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	logIDs := fileIDsWithExt(t, dir, ".log")
	if len(logIDs) != 1 {
		t.Fatalf("fixture sanity: want exactly 1 data file, got %v", logIDs)
	}
	withHint := hintFilesFor(t, dir)
	if !withHint[logIDs[0]] {
		t.Errorf("active data file %d has no hint file after Close", logIDs[0])
	}

	// The hinted reopen stays a fully working engine.
	reopened := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
}

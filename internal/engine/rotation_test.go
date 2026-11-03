package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
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

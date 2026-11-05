package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/magician336/elena/internal/engine"
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

// flipLastByte flips the last byte of the file at path in place,
// corrupting the tail entry's value without changing any length: the
// fixture for "the data file holds a checksum-failing entry that a
// scan would detect and truncate".
func flipLastByte(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%q is empty; nothing to corrupt", path)
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], info.Size()-1); err != nil {
		t.Fatalf("read last byte of %q: %v", path, err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b[:], info.Size()-1); err != nil {
		t.Fatalf("flip last byte of %q: %v", path, err)
	}
}

// dataFilePathFor returns the path of the data file with the given ID.
func dataFilePathFor(t *testing.T, dir string, id uint32) string {
	t.Helper()
	return filepath.Join(dir, fmt.Sprintf("%010d.log", id))
}

// fileSizeOf returns the current size of path.
func fileSizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.Size()
}

// Open must prefer a file's hint over scanning its data file. The
// behavioral proof: an immutable data file is corrupted in a way a
// scan must react to (a checksum-failing entry tail) — with a valid
// hint present, Open leaves the file untouched (no truncation, which
// only a scan performs) and the corrupted key stays in the index, so
// Get reports ErrCorrupted instead of the ErrNotFound a truncating
// scan would leave behind. Every other key still reads exactly.
func TestOpenPrefersHintOverDataFileScan(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	want := writeRotatedFixture(t, eng, 12)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	logIDs := fileIDsWithExt(t, dir, ".log")
	if len(logIDs) < 2 {
		t.Fatalf("fixture sanity: want >= 2 data files, got %v", logIDs)
	}

	// Corrupt the first immutable file's last entry value byte.
	immutable := dataFilePathFor(t, dir, logIDs[0])
	flipLastByte(t, immutable)
	sizeBefore := fileSizeOf(t, immutable)

	reopened := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, reopened)

	if got := fileSizeOf(t, immutable); got != sizeBefore {
		t.Fatalf("immutable data file was truncated during Open (size %d -> %d); a valid hint must spare the data file from scanning", sizeBefore, got)
	}

	corrupted := 0
	for key, value := range want {
		got, err := reopened.Get([]byte(key))
		switch {
		case err == nil:
			if string(got) != value {
				t.Errorf("Get(%q) = %q, want the exact fixture value", key, got)
			}
		case errors.Is(err, engine.ErrCorrupted):
			corrupted++ // the index still knows the key: it came from the hint
		default:
			t.Errorf("Get(%q) err = %v, want exact value or ErrCorrupted", key, err)
		}
	}
	if corrupted != 1 {
		t.Errorf("exactly 1 key should read ErrCorrupted (the flipped entry), got %d", corrupted)
	}
}

// hintPathFor returns the path of the hint file with the given ID.
func hintPathFor(t *testing.T, dir string, id uint32) string {
	t.Helper()
	return filepath.Join(dir, fmt.Sprintf("%010d.hint", id))
}

// A damaged hint (a crash can leave one behind) must never stop Open
// or lose data: the hint is discarded and the data file is scanned in
// full instead — the hint is an accelerator, not a correctness
// dependency. Every damage shape lands in the same fallback.
func TestOpenFallsBackWhenHintCorrupted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{"checksum flip", func(t *testing.T, path string) { flipLastByte(t, path) }},
		{"truncated record tail", func(t *testing.T, path string) {
			size := fileSizeOf(t, path)
			if size < 4 {
				t.Fatalf("hint %q too small to truncate", path)
			}
			if err := os.Truncate(path, size-3); err != nil { // cut into the last record
				t.Fatalf("truncate hint %q: %v", path, err)
			}
		}},
		{"emptied", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatalf("empty hint %q: %v", path, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eng := openEngineWithOptions(t, dir, smallFileOptions())

			want := writeRotatedFixture(t, eng, 12)
			if err := eng.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}
			logIDs := fileIDsWithExt(t, dir, ".log")
			if len(logIDs) < 2 {
				t.Fatalf("fixture sanity: want >= 2 data files, got %v", logIDs)
			}

			tc.damage(t, hintPathFor(t, dir, logIDs[0]))

			reopened := openEngineWithOptions(t, dir, smallFileOptions())
			defer closeEngine(t, reopened)
			assertAllReadable(t, reopened, want)
		})
	}
}

// Entries appended after a hint was written (a hint from an earlier
// Close, followed by more writes and a crash before the next Close)
// must still be recovered: Open trusts the hint only up to the offset
// it covers and scans the rest of the data file.
func TestOpenRecoversEntriesAppendedAfterHint(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	if err := eng.Put([]byte("before"), []byte("v1")); err != nil {
		t.Fatalf("Put(before) failed: %v", err)
	}
	if err := eng.Close(); err != nil { // hint now covers the whole file
		t.Fatalf("Close failed: %v", err)
	}

	// Append one real entry behind the engine's back: build it through
	// the public API in a scratch directory and copy the bytes.
	scratch := t.TempDir()
	scratchEng := openEngineWithOptions(t, scratch, engine.Options{})
	if err := scratchEng.Put([]byte("after-hint"), []byte("v2")); err != nil {
		t.Fatalf("scratch Put failed: %v", err)
	}
	if err := scratchEng.Close(); err != nil {
		t.Fatalf("scratch Close failed: %v", err)
	}
	entry := readAll(t, dataFilePath(t, scratch))
	f, err := os.OpenFile(dataFilePath(t, dir), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open data file for append: %v", err)
	}
	if _, err := f.Write(entry); err != nil {
		t.Fatalf("append entry behind engine's back: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close appended data file: %v", err)
	}

	reopened := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, reopened)
	for key, want := range map[string]string{"before": "v1", "after-hint": "v2"} {
		got, err := reopened.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q) after hinted reopen with post-hint append failed: %v", key, err)
		}
		if string(got) != want {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
}

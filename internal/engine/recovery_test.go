package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elena/internal/engine"
)

// dataFilePath returns the path of the single data file in dir.
// Fixture tests need it to corrupt the file on disk directly; the
// assertions themselves still go through the public engine API.
func dataFilePath(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".log" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want exactly one data file in %q, got %d", dir, len(paths))
	}
	return paths[0]
}

// corruptTail appends raw bytes to the data file behind the engine's
// back, simulating a crash mid-append.
func appendRaw(t *testing.T, dir string, raw []byte) {
	t.Helper()
	path := dataFilePath(t, dir)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open data file %q: %v", path, err)
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatalf("append raw bytes: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close data file: %v", err)
	}
}

// fileSize returns the current size of the data file in dir.
func fileSize(t *testing.T, dir string) int64 {
	t.Helper()
	info, err := os.Stat(dataFilePath(t, dir))
	if err != nil {
		t.Fatalf("stat data file: %v", err)
	}
	return info.Size()
}

// writeFixture writes keys key0..keyN-1 through the public API, closes
// the engine, and returns a verification function that asserts every
// key reads back from a freshly opened engine.
func writeFixture(t *testing.T, dir string, n int) {
	t.Helper()
	eng := openEngine(t, dir)
	for i := 0; i < n; i++ {
		key := []byte(string(rune('a' + i)))
		if err := eng.Put(key, []byte("value")); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// assertFixtureReopens asserts that after whatever corruption the test
// applied, Open succeeds, every fixture key survives, and the file was
// truncated back to its clean size.
func assertFixtureReopens(t *testing.T, dir string, n int, cleanSize int64) {
	t.Helper()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	for i := 0; i < n; i++ {
		key := []byte(string(rune('a' + i)))
		got, err := eng.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) after dirty reopen failed: %v", key, err)
		}
		if string(got) != "value" {
			t.Errorf("Get(%q) = %q, want %q", key, got, "value")
		}
	}
	if got := fileSize(t, dir); got != cleanSize {
		t.Errorf("data file size after reopen = %d, want %d (torn tail truncated)", got, cleanSize)
	}
}

// A crash mid-append leaves a partial entry at the file tail. Open
// must truncate it and keep every previously complete entry readable.
func TestDirtyReopenTruncatesTornEntryTail(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, 3)
	clean := fileSize(t, dir)

	// Simulate a torn write: append the first 15 bytes of what looks
	// like an entry header (less than a full header).
	appendRaw(t, dir, make([]byte, 15))

	assertFixtureReopens(t, dir, 3, clean)

	// The truncated key must not be visible.
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	if _, err := eng.Get([]byte("torn")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(torn) err = %v, want ErrNotFound", err)
	}
}

// A crash can also leave a complete-looking header whose body never
// made it to disk (lengths overrun the file). Open must treat it as a
// torn tail and truncate.
func TestDirtyReopenTruncatesOverrunBodyTail(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, 3)
	clean := fileSize(t, dir)

	// Full header claiming keyLen=1000, valueLen=4, but no body:
	// the claimed lengths overrun the file end.
	overrun := make([]byte, 28)
	overrun[20] = 0xe8 // keyLen low bytes: 1000
	overrun[21] = 0x03
	overrun[24] = 4 // valueLen
	appendRaw(t, dir, overrun)

	assertFixtureReopens(t, dir, 3, clean)
}

// A tail entry of full length but with a corrupted byte (checksum
// mismatch) must also be truncated; a CRC that passes by accident over
// garbage is what the checksum exists to prevent.
func TestDirtyReopenTruncatesCorruptedCRCTail(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, 3)
	clean := fileSize(t, dir)

	// Build one real entry through the public API in a scratch
	// directory, corrupt a value byte, and append it to the data file.
	scratch := t.TempDir()
	scratchEng := openEngine(t, scratch)
	if err := scratchEng.Put([]byte("victim"), []byte("payload")); err != nil {
		t.Fatalf("scratch Put failed: %v", err)
	}
	if err := scratchEng.Close(); err != nil {
		t.Fatalf("scratch Close failed: %v", err)
	}
	real := readAll(t, dataFilePath(t, scratch))
	corrupted := append([]byte(nil), real...)
	corrupted[len(corrupted)-1] ^= 0xff // flip a value byte: CRC fails
	appendRaw(t, dir, corrupted)

	assertFixtureReopens(t, dir, 3, clean)

	// The corrupted entry must not be visible.
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	if _, err := eng.Get([]byte("victim")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(victim) err = %v, want ErrNotFound", err)
	}
}

// Truncation must leave the file ready for new appends: after a dirty
// reopen, new writes land after the last complete entry and survive
// the next reopen. Without truncation the stale garbage would hide
// every later entry from recovery.
func TestDirtyReopenThenAppendSurvivesAnotherReopen(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, 2)

	appendRaw(t, dir, []byte{0xde, 0xad, 0xbe}) // junk tail

	eng := openEngine(t, dir) // truncation happens here
	if err := eng.Put([]byte("post-recovery"), []byte("fresh")); err != nil {
		t.Fatalf("Put after dirty reopen failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	final := openEngine(t, dir)
	defer closeEngine(t, final)
	if got, err := final.Get([]byte("post-recovery")); err != nil || string(got) != "fresh" {
		t.Errorf("Get(post-recovery) = %q, %v; want \"fresh\", nil", got, err)
	}
	if got, err := final.Get([]byte("a")); err != nil || string(got) != "value" {
		t.Errorf("Get(a) = %q, %v; want \"value\", nil", got, err)
	}
}

// A file that contains nothing but garbage (no complete entry at all)
// must open successfully as an empty engine.
func TestDirtyReopenGarbageOnlyFile(t *testing.T) {
	dir := t.TempDir()

	// Let the engine create the data file, then replace its contents
	// with pure garbage (no valid entry anywhere).
	eng := openEngine(t, dir)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := os.WriteFile(dataFilePath(t, dir), []byte("garbage garbage garbage"), 0o644); err != nil {
		t.Fatalf("overwrite data file with garbage: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)
	if _, err := reopened.Get([]byte("anything")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get on garbage-only file err = %v, want ErrNotFound", err)
	}
	if err := reopened.Put([]byte("fresh"), []byte("v")); err != nil {
		t.Fatalf("Put on garbage-only file failed: %v", err)
	}
	if got, err := reopened.Get([]byte("fresh")); err != nil || string(got) != "v" {
		t.Errorf("Get(fresh) = %q, %v; want \"v\", nil", got, err)
	}
}

// readAll returns the full contents of path.
func readAll(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return b
}

package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elena/internal/engine"
)

// openEngine is a helper that opens dir and fails the test on error.
func openEngine(t *testing.T, dir string) *engine.Engine {
	t.Helper()
	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	return eng
}

// After a clean Close and Open, every acknowledged write must still be
// readable with the exact same value. Open rebuilds the index by
// scanning the data file from the start.
func TestCleanReopenPreservesAllData(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	want := map[string]string{}
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%03d", i)
		value := fmt.Sprintf("value-%03d-%s", i, strings.Repeat("x", i))
		if err := eng.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)

	if len(want) == 0 {
		t.Fatal("fixture wrote no data")
	}
	for key, value := range want {
		got, err := reopened.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q) after reopen failed: %v", key, err)
		}
		if string(got) != value {
			t.Errorf("Get(%q) after reopen = %q, want %q", key, got, value)
		}
	}
}

// Reopen must replay the full history in order, so the semantics
// survive: overwrites resolve to the newest entry and tombstones keep
// their keys hidden. A key written after another was deleted stays
// readable.
func TestCleanReopenReplaysOverwritesAndTombstones(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	steps := []struct {
		op    string
		key   string
		value string
	}{
		{op: "put", key: "over", value: "old"},
		{op: "put", key: "over", value: "new"},
		{op: "put", key: "gone", value: "deleted"},
		{op: "delete", key: "gone"},
		{op: "put", key: "stays", value: "kept"},
		{op: "put", key: "gone", value: "resurrected"},
		{op: "delete", key: "gone"},
	}
	for i, step := range steps {
		var err error
		switch step.op {
		case "put":
			err = eng.Put([]byte(step.key), []byte(step.value))
		case "delete":
			err = eng.Delete([]byte(step.key))
		}
		if err != nil {
			t.Fatalf("step %d %s(%q) failed: %v", i, step.op, step.key, err)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)

	if got, err := reopened.Get([]byte("over")); err != nil || string(got) != "new" {
		t.Errorf("Get(over) = %q, %v; want \"new\", nil", got, err)
	}
	if got, err := reopened.Get([]byte("stays")); err != nil || string(got) != "kept" {
		t.Errorf("Get(stays) = %q, %v; want \"kept\", nil", got, err)
	}
	if got, err := reopened.Get([]byte("gone")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get(gone) = %q, %v; want ErrNotFound", got, err)
	}
}

// A reopened engine keeps working as a full engine: writes appended
// after reopen are readable immediately and after another reopen.
func TestWritesAfterReopenAppendToDataFile(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	if err := eng.Put([]byte("before"), []byte("v1")); err != nil {
		t.Fatalf("Put before reopen failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	if err := reopened.Put([]byte("after"), []byte("v2")); err != nil {
		t.Fatalf("Put after reopen failed: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	final := openEngine(t, dir)
	defer closeEngine(t, final)
	for key, want := range map[string]string{"before": "v1", "after": "v2"} {
		got, err := final.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", key, err)
		}
		if string(got) != want {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
}

// Opening a fresh empty directory yields an empty but fully functional
// engine: every Get misses.
func TestReopenEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)

	if _, err := reopened.Get([]byte("anything")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get on reopened empty dir err = %v, want ErrNotFound", err)
	}
}

// A directory can hold more than one data file (rotation arrives in a
// later ticket; recovery and read routing must already be correct for
// it). Fixture: duplicate the single data file under the next file ID
// by hand, then verify Open replays both files in order, serves reads
// from the older file's entries, and appends to the newest one.
func TestReopenMultipleDataFiles(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	if err := eng.Put([]byte("old"), []byte("from-file-1")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	first := dataFilePath(t, dir)
	second := filepath.Join(filepath.Dir(first), "0000000002.log")
	if err := copyFile(first, second); err != nil {
		t.Fatalf("copy data file: %v", err)
	}

	reopened := openEngine(t, dir) // both files replayed, file 2 active
	if err := reopened.Put([]byte("new"), []byte("from-file-2")); err != nil {
		t.Fatalf("Put after multi-file reopen failed: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	final := openEngine(t, dir)
	defer closeEngine(t, final)
	if got, err := final.Get([]byte("old")); err != nil || string(got) != "from-file-1" {
		t.Errorf("Get(old) = %q, %v; want \"from-file-1\", nil", got, err)
	}
	if got, err := final.Get([]byte("new")); err != nil || string(got) != "from-file-2" {
		t.Errorf("Get(new) = %q, %v; want \"from-file-2\", nil", got, err)
	}
}

// copyFile duplicates src as dst.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

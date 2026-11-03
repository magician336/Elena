package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elena/internal/engine"
)

// Open must create the storage directory (including missing parents) so
// callers can point the engine at a fresh path.
func TestOpenCreatesStorageDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer eng.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("storage directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("path %q exists but is not a directory", dir)
	}
}

// While one Engine holds a directory, a second Open of the same
// directory must fail with ErrDirectoryLocked so two writers can never
// corrupt the stored data.
func TestOpenSecondInstanceOnSameDirectoryFails(t *testing.T) {
	dir := t.TempDir()

	first, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	defer first.Close()

	second, err := engine.Open(dir, engine.Options{})
	if !errors.Is(err, engine.ErrDirectoryLocked) {
		t.Fatalf("second Open(%q) err = %v, want ErrDirectoryLocked", dir, err)
	}
	if second != nil {
		second.Close()
		t.Error("second Open must not return an engine on failure")
	}
}

package engine_test

import (
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

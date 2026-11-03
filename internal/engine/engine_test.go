package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elena/internal/engine"
)

// closeEngine closes eng and reports any failure through t. A nil eng
// is tolerated so failed Open calls can be cleaned up unconditionally.
func closeEngine(t *testing.T, eng *engine.Engine) {
	t.Helper()
	if eng == nil {
		return
	}
	if err := eng.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

// Open must create the storage directory (including missing parents) so
// callers can point the engine at a fresh path.
func TestOpenCreatesStorageDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

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
	defer closeEngine(t, first)

	second, err := engine.Open(dir, engine.Options{})
	if !errors.Is(err, engine.ErrDirectoryLocked) {
		t.Fatalf("second Open(%q) err = %v, want ErrDirectoryLocked", dir, err)
	}
	if second != nil {
		closeEngine(t, second)
		t.Error("second Open must not return an engine on failure")
	}
}

// Close must release the directory lock so the same directory can be
// opened again afterwards.
func TestCloseReleasesDirectoryLock(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) after Close failed: %v", dir, err)
	}
	if err := reopened.Close(); err != nil {
		t.Errorf("Close() of reopened engine failed: %v", err)
	}
}

// Close must be safe to call more than once on the same Engine.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("first Close() failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Errorf("second Close() failed: %v", err)
	}
}

// Open must fail with a clear error when the target path exists but is
// a regular file, not a directory.
func TestOpenOnNonDirectoryPathFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	eng, err := engine.Open(path, engine.Options{})
	if err == nil {
		closeEngine(t, eng)
		t.Fatal("Open on a regular file must fail")
	}
	if errors.Is(err, engine.ErrDirectoryLocked) {
		t.Errorf("Open on a regular file must not report ErrDirectoryLocked, got: %v", err)
	}
}

// Open must fail with a clear error when the directory exists but is
// not writable, so the lock file cannot be created.
func TestOpenOnUnwritableDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	eng, err := engine.Open(dir, engine.Options{})
	if err == nil {
		closeEngine(t, eng)
		t.Fatal("Open on an unwritable directory must fail")
	}
	if errors.Is(err, engine.ErrDirectoryLocked) {
		t.Errorf("Open on an unwritable directory must not report ErrDirectoryLocked, got: %v", err)
	}
}

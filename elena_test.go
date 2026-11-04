package elena_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elena"
)

// closeStore closes st and reports any failure through t. A nil st is
// tolerated so failed Open calls can be cleaned up unconditionally.
func closeStore(t *testing.T, st *elena.Store) {
	t.Helper()
	if st == nil {
		return
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

// Open must create the store directory (including missing parents) so
// callers can point a store at a fresh path.
func TestOpenCreatesStoreDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, st)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("store directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("path %q exists but is not a directory", dir)
	}
}

// While one store holds a directory, a second Open of the same
// directory must fail with an error wrapping ErrDirectoryLocked so two
// stores can never write the same directory.
func TestOpenSecondStoreOnSameDirectoryFails(t *testing.T) {
	dir := t.TempDir()

	first, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, first)

	second, err := elena.Open(dir)
	if !errors.Is(err, elena.ErrDirectoryLocked) {
		t.Fatalf("second Open(%q) err = %v, want ErrDirectoryLocked", dir, err)
	}
	if second != nil {
		closeStore(t, second)
		t.Error("second Open must not return a store on failure")
	}
}

// Close must release the directory lock so the same directory can be
// opened again afterwards.
func TestCloseReleasesDirectoryLock(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) after Close failed: %v", dir, err)
	}
	if err := reopened.Close(); err != nil {
		t.Errorf("Close() of reopened store failed: %v", err)
	}
}

// Close must be safe to call more than once on the same store.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("first Close() failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close() failed: %v", err)
	}
}

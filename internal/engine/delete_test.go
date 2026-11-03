package engine_test

import (
	"errors"
	"testing"

	"elena/internal/engine"
)

// Delete must remove the key from the reader's point of view: Get
// reports ErrNotFound afterwards. The tombstone is a durable write, so
// the deletion also survives reopening (recovery is covered in the
// reopen tests; here the live behavior is pinned).
func TestDeleteHidesKeyFromGet(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	if err := eng.Put([]byte("doomed"), []byte("value")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := eng.Delete([]byte("doomed")); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := eng.Get([]byte("doomed")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

// Deleting a key that was never written is not an error: the end state
// ("key does not exist") is already true.
func TestDeleteMissingKeySucceeds(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	if err := eng.Delete([]byte("never-existed")); err != nil {
		t.Errorf("Delete(missing) err = %v, want nil", err)
	}
}

// Delete must respect the key size limit for the same reason Put
// does: the tombstone carries the key on disk.
func TestDeleteOversizedKeyRejected(t *testing.T) {
	eng, err := engine.Open(t.TempDir(), engine.Options{MaxKeySize: 8})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer closeEngine(t, eng)

	if err := eng.Delete(bytesOfLength(9)); !errors.Is(err, engine.ErrKeyTooLarge) {
		t.Errorf("Delete(oversized) err = %v, want ErrKeyTooLarge", err)
	}
}

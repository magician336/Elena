package engine_test

import (
	"errors"
	"testing"
	"time"

	"elena/internal/engine"
)

// Sync tests share one fixture shape: a handful of puts, an overwrite,
// a delete, and one TTL entry — enough surface that a reopen must
// rebuild the exact live set. want is the expected key/value map after
// the fixture has been written; the TTL key is absent because it is
// expired by the time any assertion runs.
func syncFixtureWant() map[string]string {
	return map[string]string{
		"k-put-1":    "v1",
		"k-put-2":    "v2",
		"k-put-3":    "v3",
		"k-overwide": "second-write-wins",
	}
}

// writeSyncFixture writes the standard sync-policy fixture through the
// public API and returns the expected live set (expired TTL keys and
// deleted keys excluded).
func writeSyncFixture(t *testing.T, eng *engine.Engine) map[string]string {
	t.Helper()
	want := syncFixtureWant()
	if err := eng.Put([]byte("k-put-1"), []byte("v1")); err != nil {
		t.Fatalf("Put(k-put-1) failed: %v", err)
	}
	if err := eng.Put([]byte("k-put-2"), []byte("v2")); err != nil {
		t.Fatalf("Put(k-put-2) failed: %v", err)
	}
	if err := eng.Put([]byte("k-overwide"), []byte("first-write")); err != nil {
		t.Fatalf("Put(k-overwide) failed: %v", err)
	}
	if err := eng.Put([]byte("k-overwide"), []byte("second-write-wins")); err != nil {
		t.Fatalf("overwrite Put(k-overwide) failed: %v", err)
	}
	if err := eng.Put([]byte("k-put-3"), []byte("v3")); err != nil {
		t.Fatalf("Put(k-put-3) failed: %v", err)
	}
	if err := eng.Delete([]byte("k-put-2")); err != nil {
		t.Fatalf("Delete(k-put-2) failed: %v", err)
	}
	delete(want, "k-put-2")
	// A TTL entry that is born nearly dead: invisible to assertions, but
	// its header must survive whatever path the policy takes.
	if err := eng.PutWithTTL([]byte("k-expired"), []byte("gone"), time.Millisecond); err != nil {
		t.Fatalf("PutWithTTL(k-expired) failed: %v", err)
	}
	return want
}

// The sync policy is chosen at Open and unknown values are
// configuration errors: an out-of-range policy must fail Open with a
// clear error rather than silently falling back to a default tier, and
// the directory must stay openable afterwards.
func TestOpenRejectsUnknownSyncPolicy(t *testing.T) {
	for _, policy := range []engine.SyncPolicy{engine.SyncPolicy(42), engine.SyncPolicy(-1)} {
		dir := t.TempDir()
		eng, err := engine.Open(dir, engine.Options{SyncPolicy: policy})
		if err == nil {
			closeEngine(t, eng)
			t.Fatalf("Open with SyncPolicy(%d) succeeded, want a configuration error", policy)
		}
		if errors.Is(err, engine.ErrDirectoryLocked) {
			t.Errorf("Open with SyncPolicy(%d) reported ErrDirectoryLocked, want a configuration error", policy)
		}
		if eng != nil {
			t.Errorf("Open with SyncPolicy(%d) returned an engine on failure", policy)
		}
		// The rejected Open must leave the directory usable.
		ok := openEngineWithOptions(t, dir, engine.Options{})
		closeEngine(t, ok)
	}
}

// The zero value of Options selects SyncAlways: without any explicit
// configuration every write is fdatasync'ed before it acknowledges.
// This is the default-durability regression guard for the tier work.
func TestDefaultSyncPolicyIsAlways(t *testing.T) {
	var opts engine.Options
	if opts.SyncPolicy != engine.SyncAlways {
		t.Fatalf("Options{}.SyncPolicy = %v, want SyncAlways (the default tier)", opts.SyncPolicy)
	}
}

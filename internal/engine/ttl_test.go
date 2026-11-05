//go:build unix

package engine_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/magician336/elena/internal/engine"
)

// shortTTL is long enough to stay comfortably unexpired across an
// immediate Get, and expiryWait is a generous multiple of it so the
// "expired" assertions never race the clock.
const (
	shortTTL   = 80 * time.Millisecond
	expiryWait = 200 * time.Millisecond
)

// A value written with a TTL must be readable while the TTL is still
// live: the read path returns the exact value as with a plain Put.
func TestPutWithTTLValueReadableBeforeExpiry(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	if err := eng.PutWithTTL([]byte("user-42"), []byte("blob-1"), 10*time.Second); err != nil {
		t.Fatalf("PutWithTTL failed: %v", err)
	}

	got, err := eng.Get([]byte("user-42"))
	if err != nil {
		t.Fatalf("Get after PutWithTTL failed: %v", err)
	}
	if string(got) != "blob-1" {
		t.Errorf("Get after PutWithTTL = %q, want %q", got, "blob-1")
	}
}

// PutWithTTL must reject ttls that carry no honest meaning: zero and
// negative ones (born-expired entries) and ones so large the expiry
// timestamp would overflow the entry header's int64 nanoseconds.
func TestPutWithTTLRejectsInvalidTTL(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero ttl", 0},
		{"negative ttl", -time.Second},
		{"unrepresentable ttl", math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "ttl-" + tt.name
			err := eng.PutWithTTL([]byte(key), []byte("v"), tt.ttl)
			if !errors.Is(err, engine.ErrInvalidTTL) {
				t.Fatalf("PutWithTTL(%s) err = %v, want ErrInvalidTTL", tt.name, err)
			}
			if _, err := eng.Get([]byte(key)); !errors.Is(err, engine.ErrNotFound) {
				t.Errorf("Get after rejected PutWithTTL err = %v, want ErrNotFound (no trace)", err)
			}
		})
	}
}

// Once the ttl has elapsed, Get must treat the entry exactly like a
// never-written key: ErrNotFound, and nil value. Entries around it —
// one without a TTL, one whose TTL is still live — stay readable, so
// invisibility is per-entry, not a global clock failure.
func TestGetOnExpiredEntryReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	writes := []struct {
		key string
		ttl time.Duration // 0 = plain Put
	}{
		{"expiring", shortTTL},
		{"plain", 0},
		{"still-live", time.Hour},
	}
	for _, w := range writes {
		if w.ttl == 0 {
			err = eng.Put([]byte(w.key), []byte("v-"+w.key))
		} else {
			err = eng.PutWithTTL([]byte(w.key), []byte("v-"+w.key), w.ttl)
		}
		if err != nil {
			t.Fatalf("write %q failed: %v", w.key, err)
		}
	}

	time.Sleep(expiryWait)

	got, err := eng.Get([]byte("expiring"))
	if !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("Get(expiring) after expiry err = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Errorf("Get(expiring) after expiry value = %q, want nil", got)
	}
	for _, key := range []string{"plain", "still-live"} {
		got, err := eng.Get([]byte(key))
		if err != nil || string(got) != "v-"+key {
			t.Errorf("Get(%q) after the other entry expired = %q, %v; want %q, nil", key, got, err, "v-"+key)
		}
	}
}

// A key whose TTL entry expired is writable again: the new entry wins
// the index and, when written through plain Put, carries no expiry at
// all — an expired past must not resurrect itself.
func TestExpiredKeyCanBeRewrittenWithoutTTL(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	if err := eng.PutWithTTL([]byte("k"), []byte("old"), shortTTL); err != nil {
		t.Fatalf("PutWithTTL failed: %v", err)
	}
	time.Sleep(expiryWait)

	if err := eng.Put([]byte("k"), []byte("new")); err != nil {
		t.Fatalf("Put after expiry failed: %v", err)
	}
	time.Sleep(expiryWait) // the rewrite must not inherit the old deadline

	got, err := eng.Get([]byte("k"))
	if err != nil || string(got) != "new" {
		t.Errorf("Get after rewrite = %q, %v; want %q, nil", got, err, "new")
	}
}

// writeExpiryFixture seeds one directory with two expiring keys (one
// destined for the active file, one pushed into an immutable file by
// rotation) plus plain live keys, waits for the expiring entries to
// die, closes the engine (so every file has a hint), and returns the
// expected visible key set.
func writeExpiryFixture(t *testing.T, dir string) []string {
	t.Helper()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	if err := eng.PutWithTTL([]byte("ttl-active"), []byte("dies"), shortTTL); err != nil {
		t.Fatalf("PutWithTTL(ttl-active) failed: %v", err)
	}
	// Plain entries big enough to rotate the active file: the next TTL
	// entry lands in an immutable file that Close seals with a hint.
	live := []string{"plain-a", "plain-b"}
	for _, key := range live {
		if err := eng.Put([]byte(key), []byte("v-"+key)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
	}
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("bulk%03d", i)
		if err := eng.Put([]byte(key), []byte("value"+strings.Repeat("x", rotateFixtureValueSize))); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
		live = append(live, key)
	}
	if err := eng.PutWithTTL([]byte("ttl-immutable"), []byte("dies"), shortTTL); err != nil {
		t.Fatalf("PutWithTTL(ttl-immutable) failed: %v", err)
	}

	time.Sleep(expiryWait)

	if err := eng.Close(); err != nil {
		t.Fatalf("Close after fixture failed: %v", err)
	}
	return live
}

// assertExpiryState asserts both expiring keys stay invisible while
// every live key survives, through Get and through a full Scan.
func assertExpiryState(t *testing.T, eng *engine.Engine, live []string, label string) {
	t.Helper()
	for _, key := range []string{"ttl-active", "ttl-immutable"} {
		if got, err := eng.Get([]byte(key)); !errors.Is(err, engine.ErrNotFound) || got != nil {
			t.Errorf("%s: Get(%q) = %q, %v; want nil, ErrNotFound", label, key, got, err)
		}
	}
	want := slices.Clone(live)
	slices.Sort(want)
	var scanned []string
	if err := eng.Scan(engine.ScanOptions{}, func(key, _ []byte) error {
		scanned = append(scanned, string(key))
		return nil
	}); err != nil {
		t.Fatalf("%s: Scan failed: %v", label, err)
	}
	if !slices.Equal(scanned, want) {
		t.Errorf("%s: Scan keys = %v, want exactly the live keys %v", label, scanned, want)
	}
}

// Expiry must survive Close and Open: the expiry verdict is carried by
// the entry header on disk, so reopening — through hint files, through
// the hintless full scan, and across rotation into immutable files —
// keeps the dead entries invisible. This pins that expiry does not
// depend on any in-memory state of the first engine instance.
func TestExpiredEntriesInvisibleAfterReopen(t *testing.T) {
	t.Run("hinted reopen", func(t *testing.T) {
		dir := t.TempDir()
		live := writeExpiryFixture(t, dir) // Close wrote hints for every file

		eng := openEngineWithOptions(t, dir, smallFileOptions())
		defer closeEngine(t, eng)
		assertExpiryState(t, eng, live, "hinted reopen")
	})

	t.Run("hintless full-scan reopen", func(t *testing.T) {
		dir := t.TempDir()
		live := writeExpiryFixture(t, dir)

		for _, id := range fileIDsWithExt(t, dir, ".hint") {
			if err := os.Remove(hintPathFor(t, dir, id)); err != nil {
				t.Fatalf("remove hint %d: %v", id, err)
			}
		}

		eng := openEngineWithOptions(t, dir, smallFileOptions())
		defer closeEngine(t, eng)
		assertExpiryState(t, eng, live, "hintless reopen")
	})
}

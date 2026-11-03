package engine_test

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
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

// Every tier must keep the same Close semantics: whatever was
// acknowledged through the public API is the live set a reopen rebuilds
// — puts, overwrites, deletes, and expiry all intact. The sync policy
// trades durability windows, never the recovered content of a cleanly
// closed engine.
func TestSyncTiersSurviveCloseAndReopen(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts engine.Options
	}{
		{name: "always", opts: engine.Options{SyncPolicy: engine.SyncAlways}},
		{name: "interval", opts: engine.Options{SyncPolicy: engine.SyncInterval, SyncPeriod: time.Millisecond}},
		{name: "none", opts: engine.Options{SyncPolicy: engine.SyncNone}},
		{name: "interval-default-period", opts: engine.Options{SyncPolicy: engine.SyncInterval}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			eng := openEngineWithOptions(t, dir, tt.opts)
			want := writeSyncFixture(t, eng)

			// Everything acknowledged before Close reads back now...
			assertAllReadable(t, eng, want)
			if _, err := eng.Get([]byte("k-put-2")); err != engine.ErrNotFound {
				t.Errorf("Get(deleted k-put-2) err = %v, want ErrNotFound", err)
			}
			// ...and is exactly the live set after Close and reopen. The
			// reopen deliberately does not wait out the interval period:
			// Close must force the final flush on its own.
			if err := eng.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}
			time.Sleep(2 * time.Millisecond) // let the fixture's TTL entry expire for the reopen
			reopened := openEngineWithOptions(t, dir, tt.opts)
			defer closeEngine(t, reopened)
			assertAllReadable(t, reopened, want)
			if _, err := reopened.Get([]byte("k-put-2")); err != engine.ErrNotFound {
				t.Errorf("after reopen: Get(deleted k-put-2) err = %v, want ErrNotFound", err)
			}
			if _, err := reopened.Get([]byte("k-expired")); err != engine.ErrNotFound {
				t.Errorf("after reopen: Get(expired k-expired) err = %v, want ErrNotFound", err)
			}
		})
	}
}

// The interval policy runs a background flusher goroutine; Close must
// stop it, and stop it before Close returns — not "eventually". The
// test opens and closes a batch of interval engines whose period is
// far shorter than a Close takes (Close itself fdatasyncs and writes a
// hint while holding writeMu), so flushes land inside every Close's
// critical section — the exact interleaving where a lifecycle bug (a
// flusher outliving its engine, or a closer joining while still
// holding the lock the flusher waits for) turns into a deadlock or a
// leak. Half the rounds close immediately after a write, half after a
// few flush ticks, to cover both a busy and an idle flusher.
func TestSyncIntervalFlusherStopsOnClose(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 16; i++ {
		dir := t.TempDir()
		eng := openEngineWithOptions(t, dir, engine.Options{
			SyncPolicy: engine.SyncInterval,
			SyncPeriod: 200 * time.Microsecond,
		})
		if err := eng.Put([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("round %d: Put failed: %v", i, err)
		}
		if i%2 == 1 {
			time.Sleep(2 * time.Millisecond) // let flush ticks land mid-life
		}
		if err := eng.Close(); err != nil {
			t.Fatalf("round %d: Close failed: %v", i, err)
		}
	}
	// Close joins the flusher, so the count is back at the baseline the
	// moment Close returns; the retry loop only absorbs scheduler jitter
	// from the test binary itself.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines after 16 interval-engine Close cycles = %d, baseline was %d: the flusher leaked", after, before)
	}
}

// Close must stay race-clean when the interval flusher is in the mix:
// the flusher's fdatasync, a writer's appends, and Close all contend
// for the same file and the same locks. Every Put outcome must be nil
// or ErrClosed (never a raw I/O error from a handle the closer already
// shut), and the acknowledged seed write must survive the reopen.
func TestSyncIntervalCloseRacingWithPuts(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{
		SyncPolicy: engine.SyncInterval,
		SyncPeriod: time.Millisecond,
	})
	if err := eng.Put([]byte("seed"), []byte("v")); err != nil {
		t.Fatalf("seed Put failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			if err := eng.Put([]byte("racing"), []byte("v")); err != nil {
				if !errors.Is(err, engine.ErrClosed) {
					t.Errorf("Put during Close err = %v, want nil or ErrClosed", err)
				}
				return
			}
		}
	}()

	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	<-done

	reopened := openEngineWithOptions(t, dir, engine.Options{
		SyncPolicy: engine.SyncInterval,
		SyncPeriod: time.Millisecond,
	})
	defer closeEngine(t, reopened)
	if got, err := reopened.Get([]byte("seed")); err != nil || string(got) != "v" {
		t.Errorf("Get(seed) after interval Close race = %q, %v; want \"v\", nil", got, err)
	}
}

// Rotation under the interval policy is a forced flush point: the
// outgoing file is sealed with data and hint in agreement, and the
// whole rotated history must survive Close (which itself flushes only
// the active file) and reopen through the hint fast path. Writing
// across many rotations and closing immediately — no waiting out any
// period — is what makes Close's final flush and the rotation flushes
// load-bearing.
func TestSyncIntervalRotationsSealAndSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	opts := engine.Options{
		SyncPolicy:     engine.SyncInterval,
		SyncPeriod:     50 * time.Millisecond, // slower than the whole test body: nothing flushes on a tick
		MaxDataFileSize: 4 * (28 + 7 + rotateFixtureValueSize),
	}
	eng := openEngineWithOptions(t, dir, opts)

	want := writeRotatedFixture(t, eng, 40)
	if got := countDataFilesIn(t, dir); got < 5 {
		t.Fatalf("fixture sanity: data file count after 40 interval-tier puts = %d, want >= 5", got)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Every file must have its hint next to it after Close (rotation
	// seals each immutable file with data+hint in agreement, and Close
	// publishes the final active file's hint), so reopen takes the hint
	// fast path instead of falling back to full scans.
	if got, want := countExtIn(t, dir, ".hint"), countDataFilesIn(t, dir); got != want {
		t.Errorf("hint files after interval rotations + Close = %d, want %d (one per data file)", got, want)
	}

	reopened := openEngineWithOptions(t, dir, opts)
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
}

// The interval flusher must stay race-clean while the engine is under
// full load: one writer churning keys across real rotations, readers
// hammering seeded and fresh keys, and the flusher's fdatasync
// interleaving with both under writeMu. Under -race this is the
// broadest net for flusher lifecycle bugs short of the kill harness.
func TestSyncIntervalFlusherUnderConcurrentLoad(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{
		SyncPolicy:      engine.SyncInterval,
		SyncPeriod:      time.Millisecond,
		MaxDataFileSize: 4 * (28 + 7 + rotateFixtureValueSize),
	})
	defer closeEngine(t, eng)

	seed := writeRotatedFixture(t, eng, 12)

	const churned = 400
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < churned; i++ {
			if err := eng.Put([]byte(fmt.Sprintf("churn-%06d", i)), []byte("cv")); err != nil {
				t.Errorf("interval churn Put failed: %v", err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				for key, want := range seed {
					got, err := eng.Get([]byte(key))
					if err != nil || string(got) != want {
						t.Errorf("reader %d: Get(%q) under interval load = %q, %v; want exact fixture value", id, key, got, err)
						return
					}
				}
				n := (i + id) % churned
				got, err := eng.Get([]byte(fmt.Sprintf("churn-%06d", n)))
				if err == nil && string(got) != "cv" {
					t.Errorf("reader %d: Get(churn-%06d) = %q, want %q", id, n, got, "cv")
					return
				}
			}
		}(r)
	}

	<-writerDone
	wg.Wait()

	for i := 0; i < churned; i++ {
		if got, err := eng.Get([]byte(fmt.Sprintf("churn-%06d", i))); err != nil || string(got) != "cv" {
			t.Fatalf("Get(churn-%06d) after interval churn = %q, %v; want cv, nil", i, got, err)
		}
	}
}

package engine_test

import (
	"testing"
	"time"

	"elena/internal/engine"
)

// Batch semantics must not move with the sync policy: under every tier
// a Commit means "the whole batch is visible the moment Commit
// returns" and "the whole batch is the live set a Close+reopen
// rebuilds". What the tier changes is only when durability is forced
// (per write / per period and at Close / never) — never atomicity.
func TestBatchCommitSemanticsAcrossSyncTiers(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts engine.Options
	}{
		{name: "always", opts: engine.Options{SyncPolicy: engine.SyncAlways}},
		{name: "interval", opts: engine.Options{SyncPolicy: engine.SyncInterval, SyncPeriod: 50 * time.Millisecond}},
		{name: "none", opts: engine.Options{SyncPolicy: engine.SyncNone}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			eng := openEngineWithOptions(t, dir, tt.opts)
			defer closeEngine(t, eng)

			b := eng.NewBatch()
			if err := b.Put([]byte("b1"), []byte("v1")); err != nil {
				t.Fatalf("stage Put(b1) failed: %v", err)
			}
			if err := b.Put([]byte("b2"), []byte("v2")); err != nil {
				t.Fatalf("stage Put(b2) failed: %v", err)
			}
			if err := b.PutWithTTL([]byte("b-ttl"), []byte("v"), 20*time.Millisecond); err != nil {
				t.Fatalf("stage PutWithTTL failed: %v", err)
			}
			if err := b.Commit(); err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			// Whole batch visible immediately, before any period could
			// elapse: Commit publishes the index, sync has nothing to do
			// with visibility.
			for _, key := range []string{"b1", "b2", "b-ttl"} {
				if _, err := eng.Get([]byte(key)); err != nil {
					t.Fatalf("Get(%q) right after Commit err = %v, want the batch visible immediately", key, err)
				}
			}

			// Closing immediately (no waiting out any interval period)
			// must still land the whole committed batch in the reopened
			// engine; the TTL entry is expired by then.
			if err := eng.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}
			time.Sleep(25 * time.Millisecond)

			reopened := openEngineWithOptions(t, dir, tt.opts)
			defer closeEngine(t, reopened)
			for key, want := range map[string]string{"b1": "v1", "b2": "v2"} {
				got, err := reopened.Get([]byte(key))
				if err != nil || string(got) != want {
					t.Errorf("after reopen: Get(%q) = %q, %v; want %q, nil", key, got, err, want)
				}
			}
			if _, err := reopened.Get([]byte("b-ttl")); err != engine.ErrNotFound {
				t.Errorf("after reopen: Get(expired b-ttl) err = %v, want ErrNotFound", err)
			}
		})
	}
}

// A committed batch must drive the automatic merge exactly like a
// single write does: when a Commit pushes the dead share past
// Options.MergeDeadRatio, a merge runs before Commit returns. (Ticket
// 06 had left Commit off the auto-merge path because Merge needs
// writeMu itself; running the threshold check after Commit releases
// the write lock makes both fit.)
func TestBatchCommitTriggersAutoMerge(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{MergeDeadRatio: 0.5})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k1", "v1")
	mustPut(t, eng, "k2", "v2")
	mustPut(t, eng, "k1", "v1-longer") // dead share ~0.31: below the threshold
	if s := eng.Stats(); s.OpsMerge != 0 {
		t.Fatalf("OpsMerge = %d below threshold, want 0 (no auto merge yet)", s.OpsMerge)
	}

	b := eng.NewBatch()
	if err := b.Delete([]byte("k1")); err != nil {
		t.Fatalf("stage Delete(k1) failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	s := eng.Stats()
	if s.OpsMerge == 0 {
		t.Fatal("OpsMerge = 0 after a batch crossed the dead-ratio threshold, want the batch commit to have merged automatically")
	}
	if s.DeadEntries != 0 {
		t.Errorf("DeadEntries = %d after auto merge, want 0", s.DeadEntries)
	}
	if got, err := eng.Get([]byte("k2")); err != nil || string(got) != "v2" {
		t.Errorf("Get(k2) after batch-triggered auto merge = %q, %v; want v2, nil", got, err)
	}
	if _, err := eng.Get([]byte("k1")); err != engine.ErrNotFound {
		t.Errorf("Get(k1) after batch-triggered auto merge err = %v, want ErrNotFound", err)
	}
}

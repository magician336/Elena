package engine

import (
	"fmt"
	"sync"
	"testing"
)

// The sharded index unit seam (package-internal, like
// merge_close_internal_test.go): these tests pin the shard structure's
// own contract — mapping, accounting, and lock discipline. Everything
// user-visible is pinned by the public engine API tests; these are the
// internals the sharding restructure could silently break.

func TestIndexShardsEmpty(t *testing.T) {
	ix := newIndexShards()
	if got := ix.count(); got != 0 {
		t.Fatalf("count of fresh index = %d, want 0", got)
	}
	if got := ix.liveTotal(); got != 0 {
		t.Fatalf("liveTotal of fresh index = %d, want 0", got)
	}
	if got := ix.keyBytes(); got != 0 {
		t.Fatalf("keyBytes of fresh index = %d, want 0", got)
	}
	if _, ok := ix.get("missing"); ok {
		t.Fatalf("get on fresh index reported a hit")
	}
}

func TestIndexShardsApplyGet(t *testing.T) {
	ix := newIndexShards()

	r1 := entryRef{fileID: 1, offset: 10, size: 100}
	ix.apply("alpha", r1)
	got, ok := ix.get("alpha")
	if !ok || got != r1 {
		t.Fatalf("get after apply = (%v, %v), want (%v, true)", got, ok, r1)
	}
	if live := ix.liveTotal(); live != 100 {
		t.Fatalf("liveTotal after one apply = %d, want 100", live)
	}

	// Overwriting replaces the location and keeps live bytes exact:
	// the old entry's size must leave the total when the new one enters.
	r2 := entryRef{fileID: 2, offset: 0, size: 30}
	ix.apply("alpha", r2)
	if got, ok := ix.get("alpha"); !ok || got != r2 {
		t.Fatalf("get after overwrite = (%v, %v), want (%v, true)", got, ok, r2)
	}
	if live := ix.liveTotal(); live != 30 {
		t.Fatalf("liveTotal after overwrite = %d, want 30 (old size replaced, not added)", live)
	}
}

func TestIndexShardsDrop(t *testing.T) {
	ix := newIndexShards()
	ix.apply("alpha", entryRef{fileID: 1, offset: 0, size: 100})
	ix.apply("beta", entryRef{fileID: 1, offset: 100, size: 50})

	if dropped := ix.drop("alpha"); !dropped {
		t.Fatalf("drop of an existing key reported nothing dropped")
	}
	if _, ok := ix.get("alpha"); ok {
		t.Fatalf("get after drop still reports the key")
	}
	if live := ix.liveTotal(); live != 50 {
		t.Fatalf("liveTotal after drop = %d, want 50 (dropped size left the total)", live)
	}
	if n := ix.count(); n != 1 {
		t.Fatalf("count after drop = %d, want 1", n)
	}

	if dropped := ix.drop("alpha"); dropped {
		t.Fatalf("drop of a missing key reported a drop")
	}
	if live := ix.liveTotal(); live != 50 {
		t.Fatalf("liveTotal after no-op drop = %d, want 50 (unchanged)", live)
	}
}

func TestIndexShardsEachInstantCompleteness(t *testing.T) {
	ix := newIndexShards()
	const n = 1000
	want := make(map[string]entryRef, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("key-%06d", i)
		ref := entryRef{fileID: uint32(i % 7), offset: int64(i * 10), size: uint32(i%97) + 1}
		ix.apply(key, ref)
		want[key] = ref
	}

	got := make(map[string]entryRef, n)
	ix.eachInstant(func(key string, ref entryRef) {
		if _, dup := got[key]; dup {
			t.Fatalf("snapshot visited key %q twice", key)
		}
		got[key] = ref
	})
	if len(got) != n {
		t.Fatalf("snapshot visited %d keys, want %d", len(got), n)
	}
	for key, ref := range want {
		if got[key] != ref {
			t.Fatalf("snapshot ref for %q = %v, want %v", key, got[key], ref)
		}
	}

	// The shard function must actually spread the key space: a hash
	// that funnels every key into few shards would re-create the
	// single-mutex contention this structure exists to remove.
	for i := range ix {
		if len(ix[i].m) == 0 {
			t.Fatalf("shard %d of %d holds no key after %d distinct keys", i, indexShardCount, n)
		}
	}
}

func TestIndexShardsRekey(t *testing.T) {
	ix := newIndexShards()
	// Keys in merged files 1 and 2 (survivor "keep" moves, expired
	// "gone" disappears), one key in the successor active file 9
	// (a writer raced past the freeze: must stay untouched), one key
	// already absent from the rewrite output.
	ix.apply("m-keep", entryRef{fileID: 1, offset: 0, size: 100})
	ix.apply("m-gone", entryRef{fileID: 2, offset: 0, size: 40})
	ix.apply("m-keep2", entryRef{fileID: 2, offset: 40, size: 60})
	ix.apply("s-raced", entryRef{fileID: 9, offset: 0, size: 10})

	merged := map[uint32]struct{}{1: {}, 2: {}}
	newRefs := map[string]entryRef{
		"m-keep":  {fileID: 7, offset: 0, size: 100},
		"m-keep2": {fileID: 7, offset: 100, size: 60},
	}
	ix.rekey(merged, newRefs)

	if ref, ok := ix.get("m-keep"); !ok || ref != newRefs["m-keep"] {
		t.Fatalf("m-keep after rekey = (%v, %v), want the rewrite location", ref, ok)
	}
	if ref, ok := ix.get("m-keep2"); !ok || ref != newRefs["m-keep2"] {
		t.Fatalf("m-keep2 after rekey = (%v, %v), want the rewrite location", ref, ok)
	}
	if _, ok := ix.get("m-gone"); ok {
		t.Fatalf("m-gone still present after rekey (rewrite dropped it)")
	}
	if ref, ok := ix.get("s-raced"); !ok || ref != (entryRef{fileID: 9, offset: 0, size: 10}) {
		t.Fatalf("s-raced after rekey = (%v, %v), want untouched successor location", ref, ok)
	}
	// Live bytes: 100 + 60 (rewritten) + 10 (raced successor) = 170;
	// the dropped key's 40 bytes must be gone.
	if live := ix.liveTotal(); live != 170 {
		t.Fatalf("liveTotal after rekey = %d, want 170", live)
	}
	if n := ix.count(); n != 3 {
		t.Fatalf("count after rekey = %d, want 3", n)
	}
}

func TestIndexShardsWithAllLocked(t *testing.T) {
	ix := newIndexShards()
	// A whole batch of puts and tombstones applied under one
	// withAllLocked section must land exactly as sequential
	// self-locking applies would (staging order, last write wins,
	// live bytes exact).
	ix.apply("pre-existing", entryRef{fileID: 1, offset: 0, size: 9})
	ix.withAllLocked(func() {
		ix.applyLocked("b-one", entryRef{fileID: 1, offset: 100, size: 20})
		ix.applyLocked("b-two", entryRef{fileID: 1, offset: 120, size: 30})
		ix.dropLocked("pre-existing")
		ix.applyLocked("b-one", entryRef{fileID: 1, offset: 150, size: 5}) // overwrite inside the batch
	})

	if ref, ok := ix.get("b-one"); !ok || ref.size != 5 {
		t.Fatalf("b-one = (%v, %v), want the in-batch overwrite (size 5)", ref, ok)
	}
	if ref, ok := ix.get("b-two"); !ok || ref.size != 30 {
		t.Fatalf("b-two = (%v, %v), want size 30", ref, ok)
	}
	if _, ok := ix.get("pre-existing"); ok {
		t.Fatalf("pre-existing still present after locked drop")
	}
	if live := ix.liveTotal(); live != 35 {
		t.Fatalf("liveTotal after batch section = %d, want 35 (5 + 30; dropped 9 gone)", live)
	}
	if n := ix.count(); n != 2 {
		t.Fatalf("count after batch section = %d, want 2", n)
	}
}

// TestIndexShardsConcurrentIterateAndPublish pins the lock discipline
// under the race detector: whole-index snapshots and single-key
// publications interleaving on all shards. Overwrites must never change
// the key count, and quiescent live bytes must equal the sum of the
// locations the shards actually hold.
func TestIndexShardsConcurrentIterateAndPublish(t *testing.T) {
	ix := newIndexShards()
	const keys = 512
	for i := 0; i < keys; i++ {
		ix.apply(fmt.Sprintf("key-%06d", i), entryRef{fileID: 1, offset: int64(i), size: uint32(i%251) + 1})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50000; i++ {
			key := fmt.Sprintf("key-%06d", i%keys)
			ix.apply(key, entryRef{fileID: 2, offset: int64(i), size: uint32(i%131) + 1})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			seen := 0
			ix.eachInstant(func(string, entryRef) { seen++ })
			if seen != keys {
				t.Errorf("snapshot saw %d keys, want %d (snapshot must be one whole-index instant)", seen, keys)
				return
			}
		}
	}()
	wg.Wait()

	if n := ix.count(); n != keys {
		t.Fatalf("count after concurrent overwrites = %d, want %d (overwrites replace, never add)", n, keys)
	}
	var sum uint64
	for i := 0; i < keys; i++ {
		ref, ok := ix.get(fmt.Sprintf("key-%06d", i))
		if !ok {
			t.Fatalf("key %d vanished under concurrent overwrites", i)
		}
		sum += uint64(ref.size)
	}
	if live := ix.liveTotal(); live != sum {
		t.Fatalf("liveTotal = %d, want %d (must equal the sum of held locations)", live, sum)
	}
}

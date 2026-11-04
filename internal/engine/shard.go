package engine

import "sync"

// The sharded in-memory index. The engine's index was a single map
// under one global RWMutex; measurements under concurrent mixed
// read/write loads (ticket 13) attributed the dominant share of lock
// contention to that single mutex: every append's index publication
// took the write lock and stalled all in-flight readers, because a
// pending writer makes every new reader queue behind it. Splitting the
// key space into shards, each with its own mutex, bounds a publication
// to one shard and the readers it can stall to the readers of that
// shard alone.
//
// Lock discipline: shard locks are always the innermost locks of the
// engine. A path that holds the engine-global mu takes shard locks
// while holding mu (never the other way around), and the write path
// publishes under a shard lock alone (it holds writeMu, never mu). See
// Engine's concurrency-model comment for the full order.

// indexShardCount is the fixed number of index shards. It is a power
// of two (so shard selection is one hash and one mask) sized above the
// core count of the benchmark environment (24 cores at the time of the
// measurements): with at least one shard per concurrently running
// reader, distinct readers usually occupy distinct shards, so the
// per-shard reader counts an append's publication must wait for stay
// near one. More shards cost only a mutex and a map header each; fewer
// would re-concentrate the measured reader stalls.
const indexShardCount = 32

// indexShardMask selects a shard from a key's hash.
const indexShardMask = indexShardCount - 1

// indexShard is one shard of the key space: its own map and lock, plus
// the live-bytes share of the entries the map points at (the engine's
// liveBytes total is the sum over shards).
type indexShard struct {
	mu   sync.RWMutex
	m    map[string]entryRef
	live uint64
}

// indexShards is the engine's whole in-memory index.
type indexShards []indexShard

// newIndexShards creates an empty sharded index.
func newIndexShards() indexShards {
	ix := make(indexShards, indexShardCount)
	for i := range ix {
		ix[i].m = make(map[string]entryRef)
	}
	return ix
}

// shardFor returns the shard responsible for key. Shard assignment is
// a pure function of the key bytes (FNV-1a), so every path — lookup,
// publication, replay, merge swap — agrees on where a key lives within
// an engine's lifetime.
func (ix indexShards) shardFor(key string) *indexShard {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return &ix[h&indexShardMask]
}

// get returns the entry location stored for key.
func (ix indexShards) get(key string) (entryRef, bool) {
	s := ix.shardFor(key)
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.m[key]
	return ref, ok
}

// apply stores ref as key's location, replacing any previous one, and
// keeps the shard's live bytes exact: an overwritten entry's size
// leaves the total as the new one enters. This is the publication step
// of every write path (single put, delete-replay counterpart drop,
// batch commit, replay, merge swap).
func (ix indexShards) apply(key string, ref entryRef) {
	s := ix.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.m[key]; ok {
		s.live -= uint64(old.size)
	}
	s.m[key] = ref
	s.live += uint64(ref.size)
}

// drop removes key's location if one exists, returning whether a
// location was dropped. The shard's live bytes lose the dropped size.
func (ix indexShards) drop(key string) bool {
	s := ix.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.m[key]
	if !ok {
		return false
	}
	s.live -= uint64(old.size)
	delete(s.m, key)
	return true
}

// count returns the total number of keys across shards.
func (ix indexShards) count() int {
	n := 0
	for i := range ix {
		ix[i].mu.RLock()
		n += len(ix[i].m)
		ix[i].mu.RUnlock()
	}
	return n
}

// liveTotal returns the summed live bytes across shards.
func (ix indexShards) liveTotal() uint64 {
	var live uint64
	for i := range ix {
		ix[i].mu.RLock()
		live += ix[i].live
		ix[i].mu.RUnlock()
	}
	return live
}

// keyBytes returns the summed key-byte count across shards, the raw
// material of Stats.IndexMemoryBytes.
func (ix indexShards) keyBytes() uint64 {
	var n uint64
	for i := range ix {
		ix[i].mu.RLock()
		for key := range ix[i].m {
			n += uint64(len(key))
		}
		ix[i].mu.RUnlock()
	}
	return n
}

// eachInstant visits every key and location under all shard read
// locks held together, so fn observes one whole-index instant: writes
// published after this call starts are invisible to it, exactly as the
// single-lock index once guaranteed with one mutex. The locks are
// taken and released in shard order; fn must not acquire any shard
// lock itself (nor the engine's global mu after releasing shard locks
// in inverted order — canonical engine order is global mu first, then
// shard locks, which is how every caller reaches this method).
func (ix indexShards) eachInstant(fn func(key string, ref entryRef)) {
	for i := range ix {
		ix[i].mu.RLock()
	}
	for i := range ix {
		for key, ref := range ix[i].m {
			fn(key, ref)
		}
	}
	for i := range ix {
		ix[i].mu.RUnlock()
	}
}

// eachShardWrite hands fn one shard at a time under that shard's write
// lock, in shard order. It is the merge swap's iteration: the swap runs
// while the caller holds the engine's global mu write lock (readers
// cannot be inside), so taking the shard write locks one by one loses
// no atomicity while never holding more than one at a time.
func (ix indexShards) eachShardWrite(fn func(m map[string]entryRef, live *uint64)) {
	for i := range ix {
		s := &ix[i]
		s.mu.Lock()
		fn(s.m, &s.live)
		s.mu.Unlock()
	}
}

// withAllLocked holds every shard's write lock together, runs fn, and
// releases them. It is the batch commit's publication: one batch is one
// index publication, so a concurrent whole-index snapshot (which holds
// every shard read lock) sees the batch whole or not at all — the
// atomicity a single mu.Lock once gave the whole index, now paid once
// per batch instead of once per append. The caller must hold writeMu
// (like every publisher) and must not touch the engine's global mu
// inside fn.
func (ix indexShards) withAllLocked(fn func()) {
	for i := range ix {
		ix[i].mu.Lock()
	}
	defer func() {
		for i := range ix {
			ix[i].mu.Unlock()
		}
	}()
	fn()
}

// applyLocked stores ref as key's location without taking the shard
// lock: the caller holds every shard's write lock (withAllLocked).
func (ix indexShards) applyLocked(key string, ref entryRef) {
	s := ix.shardFor(key)
	if old, ok := s.m[key]; ok {
		s.live -= uint64(old.size)
	}
	s.m[key] = ref
	s.live += uint64(ref.size)
}

// dropLocked removes key's location without taking the shard lock: the
// caller holds every shard's write lock (withAllLocked).
func (ix indexShards) dropLocked(key string) {
	s := ix.shardFor(key)
	if old, ok := s.m[key]; ok {
		s.live -= uint64(old.size)
		delete(s.m, key)
	}
}

// rekey applies the merge swap to the whole index: every key whose
// current location sits in one of the merged files moves to its rewrite
// location from newRefs, or disappears when the rewrite dropped it (the
// entry expired before or during the rewrite). Keys located in other
// files — the successor active file a racing writer may have reached —
// stay exactly as they are. Live bytes stay exact across the swap. The
// caller must hold the engine's global mu write lock (with writeMu):
// that is what makes the per-shard rewrites one atomic switch for
// readers.
func (ix indexShards) rekey(mergedSet map[uint32]struct{}, newRefs map[string]entryRef) {
	ix.eachShardWrite(func(m map[string]entryRef, live *uint64) {
		for key, ref := range m {
			if _, old := mergedSet[ref.fileID]; !old {
				continue
			}
			*live -= uint64(ref.size)
			if newRef, ok := newRefs[key]; ok {
				m[key] = newRef
				*live += uint64(newRef.size)
			} else {
				delete(m, key)
			}
		}
	})
}

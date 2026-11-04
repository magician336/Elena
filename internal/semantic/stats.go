package semantic

import "elena/internal/engine"

// FileStats describes one data file of the store directory as Stats
// sees it: the engine's per-file observables, forwarded as-is.
type FileStats = engine.FileStats

// Stats is a point-in-time view of the store's disk observables —
// the engine's existing observables, forwarded unchanged: the semantic
// layer defines no metrics of its own. The dead-data view is
// byte-based, matching what a merge can actually reclaim; the two byte
// counters form the write-amplification pair. Note that entries whose
// TTL has elapsed but which no merge has reclaimed yet still count as
// live here: visibility and occupancy are separate facts.
type Stats struct {
	// LiveEntries is the number of keys currently visible to readers
	// (the in-memory index size).
	LiveEntries uint64

	// DeadEntries is the number of encoded entries on disk that no
	// reader can ever reach again: overwritten values, tombstones, and
	// everything a merge will reclaim.
	DeadEntries uint64

	// LiveBytes is the encoded size of the entries the index points at.
	LiveBytes uint64

	// DataBytes is the total size of all data files on disk.
	DataBytes uint64

	// DeadBytes is DataBytes minus LiveBytes: what a merge reclaims.
	DeadBytes uint64

	// DeadRatio is DeadBytes / DataBytes, or 0 while no data files hold
	// bytes.
	DeadRatio float64

	// IndexMemoryBytes estimates the in-memory index footprint: key
	// bytes plus a fixed per-entry overhead. An estimate, monotone in
	// the key set, not an allocator-exact figure.
	IndexMemoryBytes uint64

	// BytesWritten is the cumulative logical payload (key + value
	// bytes) accepted by writes.
	BytesWritten uint64

	// BytesFlushed is the cumulative encoded entry size appended to
	// data files: every entry header, every tombstone, and every entry
	// a merge rewrote.
	BytesFlushed uint64

	// WriteAmplification is BytesFlushed / BytesWritten, or 0 while no
	// logical bytes have been written.
	WriteAmplification float64

	// OpsPut, OpsPutWithTTL, OpsDelete, OpsGet, OpsScan, OpsMerge count
	// the write and read operations that went through the storage
	// engine's public API, successful or not.
	OpsPut        uint64
	OpsPutWithTTL uint64
	OpsDelete     uint64
	OpsGet        uint64
	OpsScan       uint64
	OpsMerge      uint64

	// Files lists every data file of the directory, sorted by file ID.
	Files []FileStats
}

// Stats returns a point-in-time view of the store's observables.
// The figures are the storage engine's own, forwarded one-to-one —
// reading Stats observes the store, it never changes it.
func (s *Store) Stats() Stats {
	return Stats(s.eng.Stats())
}

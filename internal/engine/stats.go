package engine

import (
	"sort"
)

// indexEntryOverhead is the fixed per-key cost the in-memory index
// pays beyond the key bytes themselves: a Go string header (16 bytes)
// plus the entryRef value (16 bytes padded) plus an approximate share
// of the map's bucket and tophash overhead. It is an estimate, not an
// accounting of exact allocator behavior; Stats reports it so index
// memory is a visible, monotone quantity, not a precise one.
const indexEntryOverhead = 48

// FileStats describes one data file of the engine directory as seen by
// Stats.
type FileStats struct {
	// FileID is the file's monotonic ID; the name on disk is the
	// zero-padded ID plus ".log".
	FileID uint32

	// SizeBytes is the file's current size in bytes.
	SizeBytes int64

	// Entries is the number of encoded entries the file holds, live
	// and dead alike.
	Entries uint64

	// Active reports whether this file currently receives appends.
	Active bool
}

// Stats is a point-in-time snapshot of the engine's observables — the
// single internal observation port tests and benchmarks assert
// against.
//
// The dead-data view is byte-based, matching what a merge can actually
// reclaim: DataBytes is everything the data files hold, LiveBytes is
// the encoded size of the entries the index still points at, and the
// difference is dead — overwritten values, tombstones, and entries a
// merge will drop. DeadEntries is the same story told in entries.
//
// Two byte counters form the write-amplification pair: BytesWritten
// counts the logical key+value bytes accepted by writes, BytesFlushed
// counts the encoded entry bytes actually appended to data files
// (entry headers included, tombstones and merge rewrites included),
// and WriteAmplification is their ratio.
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
	// bytes. This is the ratio Options.MergeDeadRatio compares against.
	DeadRatio float64

	// IndexMemoryBytes estimates the in-memory index footprint: key
	// bytes plus a fixed per-entry overhead. An estimate, monotone in
	// the key set, not an allocator-exact figure.
	IndexMemoryBytes uint64

	// BytesWritten is the cumulative logical payload (key + value
	// bytes) accepted by Put and PutWithTTL. Deletes carry no logical
	// payload.
	BytesWritten uint64

	// BytesFlushed is the cumulative encoded entry size appended to
	// data files: every entry header, every tombstone, and every
	// entry a merge rewrote.
	BytesFlushed uint64

	// WriteAmplification is BytesFlushed / BytesWritten, or 0 while no
	// logical bytes have been written.
	WriteAmplification float64

	// OpsPut, OpsPutWithTTL, OpsDelete, OpsGet, OpsMerge count the
	// operations that went through the public API, successful or not
	// (a Get that answers ErrNotFound still happened). OpsMerge counts
	// merges that actually ran — a no-op merge on an empty engine does
	// not count.
	OpsPut        uint64
	OpsPutWithTTL uint64
	OpsDelete     uint64
	OpsGet        uint64
	OpsMerge      uint64

	// Files lists every data file of the directory, sorted by file ID.
	Files []FileStats
}

// Stats returns a point-in-time snapshot of the engine's observables.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	s := Stats{
		LiveEntries:   uint64(len(e.index)),
		BytesWritten:  e.bytesWritten.Load(),
		BytesFlushed:  e.bytesFlushed.Load(),
		OpsPut:        e.opsPut.Load(),
		OpsPutWithTTL: e.opsPutWithTTL.Load(),
		OpsDelete:     e.opsDelete.Load(),
		OpsGet:        e.opsGet.Load(),
		OpsMerge:      e.opsMerge.Load(),
	}
	if s.BytesWritten > 0 {
		s.WriteAmplification = float64(s.BytesFlushed) / float64(s.BytesWritten)
	}

	var totalEntries uint64
	s.Files = e.fileStatsLocked()
	for _, fs := range s.Files {
		if fs.SizeBytes > 0 {
			s.DataBytes += uint64(fs.SizeBytes)
		}
		totalEntries += fs.Entries
	}
	if totalEntries > s.LiveEntries {
		s.DeadEntries = totalEntries - s.LiveEntries
	}

	var keyBytes uint64
	for key := range e.index {
		keyBytes += uint64(len(key))
	}
	s.LiveBytes = e.liveBytes
	s.IndexMemoryBytes = s.LiveEntries*indexEntryOverhead + keyBytes
	if s.DataBytes > s.LiveBytes {
		s.DeadBytes = s.DataBytes - s.LiveBytes
	}
	if s.DataBytes > 0 {
		s.DeadRatio = float64(s.DeadBytes) / float64(s.DataBytes)
	}
	return s
}

// fileStatsLocked snapshots the per-file table from the incrementally
// maintained counters (no syscalls: the counters are updated wherever
// the files change). The caller must hold mu.
func (e *Engine) fileStatsLocked() []FileStats {
	files := make([]FileStats, 0, len(e.files))
	for id, df := range e.files {
		files = append(files, FileStats{FileID: id, SizeBytes: df.bytes, Entries: df.entries, Active: id == e.activeID})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FileID < files[j].FileID })
	return files
}

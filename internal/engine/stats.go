package engine

import (
	"sort"
)

// FileStats describes one data file of the engine directory as seen by
// Stats.
type FileStats struct {
	// FileID is the file's monotonic ID; the name on disk is the
	// zero-padded ID plus ".log".
	FileID uint32

	// SizeBytes is the file's current size in bytes.
	SizeBytes int64

	// Active reports whether this file currently receives appends.
	Active bool
}

// Stats is a point-in-time snapshot of the engine's observables — the
// single internal observation port tests and benchmarks assert
// against. Two byte counters form the write-amplification pair:
// BytesWritten counts the logical key+value bytes accepted by writes,
// BytesFlushed counts the encoded entry bytes actually appended to
// data files (entry headers included, tombstones and merge rewrites
// included), and WriteAmplification is their ratio.
type Stats struct {
	// LiveEntries is the number of keys currently visible to readers
	// (the in-memory index size).
	LiveEntries uint64

	// DeadEntries is the number of encoded entries on disk that no
	// reader can ever reach again: overwritten values, tombstones, and
	// everything a merge will reclaim.
	DeadEntries uint64

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

	// OpsPut, OpsPutWithTTL, OpsDelete, OpsGet count the operations
	// that went through the public API, successful or not (a Get that
	// answers ErrNotFound still happened).
	OpsPut        uint64
	OpsPutWithTTL uint64
	OpsDelete     uint64
	OpsGet        uint64

	// Files lists every data file of the directory, sorted by file ID.
	Files []FileStats
}

// Stats returns a point-in-time snapshot of the engine's observables.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	s := Stats{
		LiveEntries:   uint64(len(e.index)),
		DeadEntries:   0, // dead-entry accounting arrives with merge
		BytesWritten:  e.bytesWritten.Load(),
		BytesFlushed:  e.bytesFlushed.Load(),
		OpsPut:        e.opsPut.Load(),
		OpsPutWithTTL: e.opsPutWithTTL.Load(),
		OpsDelete:     e.opsDelete.Load(),
		OpsGet:        e.opsGet.Load(),
	}
	if s.BytesWritten > 0 {
		s.WriteAmplification = float64(s.BytesFlushed) / float64(s.BytesWritten)
	}
	s.Files = e.fileStatsLocked()
	return s
}

// fileStatsLocked snapshots the per-file table. The caller must hold mu.
func (e *Engine) fileStatsLocked() []FileStats {
	files := make([]FileStats, 0, len(e.files))
	for id, f := range e.files {
		info, err := f.Stat()
		if err != nil {
			// A file whose size cannot be stat'd is reported with the
			// error surfaced through its size; the engine itself will
			// fail the read path long before this matters here.
			files = append(files, FileStats{FileID: id, SizeBytes: -1, Active: id == e.activeID})
			continue
		}
		files = append(files, FileStats{FileID: id, SizeBytes: info.Size(), Active: id == e.activeID})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FileID < files[j].FileID })
	return files
}

package engine_test

import (
	"fmt"
	"testing"
	"time"

	"elena/internal/engine"
)

// encodedEntrySize is the documented on-disk footprint of one entry:
// a 28-byte fixed header (CRC + timestamp + expiry + key length +
// value length) plus key and value bytes. Tombstones carry no value.
// The layout is public in the spec, so tests may rely on it.
const encodedEntrySize = 28

// Stats must count every operation that went through the public API:
// each Put, PutWithTTL, Get (hits and misses alike), and Delete. This
// is the observation port the later benchmarks build on, so the counts
// must match the calls exactly.
func TestStatsCountsOperations(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k1", "v1")
	mustPutWithTTL(t, eng, "k2", "v2", time.Hour)
	if _, err := eng.Get([]byte("k1")); err != nil {
		t.Fatalf("Get(k1) failed: %v", err)
	}
	if _, err := eng.Get([]byte("missing")); err != engine.ErrNotFound {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
	if err := eng.Delete([]byte("k1")); err != nil {
		t.Fatalf("Delete(k1) failed: %v", err)
	}

	s := eng.Stats()
	if s.OpsPut != 1 {
		t.Errorf("OpsPut = %d, want 1", s.OpsPut)
	}
	if s.OpsPutWithTTL != 1 {
		t.Errorf("OpsPutWithTTL = %d, want 1", s.OpsPutWithTTL)
	}
	if s.OpsGet != 2 {
		t.Errorf("OpsGet = %d, want 2 (hits and misses both count)", s.OpsGet)
	}
	if s.OpsDelete != 1 {
		t.Errorf("OpsDelete = %d, want 1", s.OpsDelete)
	}
}

// Stats must expose the write-amplification pair: BytesWritten counts
// the logical key+value bytes accepted by Put/PutWithTTL (deletes carry
// no logical payload), BytesFlushed counts the encoded entry bytes
// actually appended to data files — entries, their headers, and
// tombstones. WriteAmplification is the ratio of the two, so header
// overhead alone already shows up as amplification above 1.
func TestStatsWriteAmplificationBytes(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k1", "v1")                      // logical 4, encoded 28+4
	mustPutWithTTL(t, eng, "k2", "v2", time.Hour)    // logical 4, encoded 28+4
	if err := eng.Delete([]byte("k1")); err != nil { // tombstone: no logical bytes
		t.Fatalf("Delete(k1) failed: %v", err)
	}

	s := eng.Stats()
	if want := uint64(2 + 2 + 2 + 2); s.BytesWritten != want {
		t.Errorf("BytesWritten = %d, want %d (key+value bytes of the two puts)", s.BytesWritten, want)
	}
	if want := uint64(2*(encodedEntrySize+4) + encodedEntrySize + 2); s.BytesFlushed != want {
		t.Errorf("BytesFlushed = %d, want %d (two value entries plus one tombstone)", s.BytesFlushed, want)
	}
	wantAmp := float64(s.BytesFlushed) / float64(s.BytesWritten)
	if s.WriteAmplification != wantAmp {
		t.Errorf("WriteAmplification = %v, want %v (BytesFlushed/BytesWritten)", s.WriteAmplification, wantAmp)
	}
	if s.WriteAmplification <= 1 {
		t.Errorf("WriteAmplification = %v, want > 1 (entry headers and tombstones amplify)", s.WriteAmplification)
	}
}

// Stats on a fresh engine must be a coherent zero state: no entries, no
// bytes, no amplification, and exactly one (active) file.
func TestStatsEmptyEngine(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	s := eng.Stats()
	if s.LiveEntries != 0 || s.DeadEntries != 0 {
		t.Errorf("entries on a fresh engine: live = %d, dead = %d, want 0/0", s.LiveEntries, s.DeadEntries)
	}
	if s.BytesWritten != 0 || s.BytesFlushed != 0 {
		t.Errorf("byte counters on a fresh engine: written = %d, flushed = %d, want 0/0", s.BytesWritten, s.BytesFlushed)
	}
	if s.WriteAmplification != 0 {
		t.Errorf("WriteAmplification with no writes = %v, want 0 (undefined, reported as 0)", s.WriteAmplification)
	}
	if len(s.Files) != 1 {
		t.Fatalf("Files on a fresh engine has %d entries, want 1 (the active file)", len(s.Files))
	}
	if !s.Files[0].Active {
		t.Errorf("the single file of a fresh engine is not marked active: %+v", s.Files[0])
	}
}

// mustPut puts one pair through the public API.
func mustPut(t *testing.T, eng *engine.Engine, key, value string) {
	t.Helper()
	if err := eng.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q) failed: %v", key, err)
	}
}

// mustPutWithTTL puts one pair with a TTL through the public API.
func mustPutWithTTL(t *testing.T, eng *engine.Engine, key, value string, ttl time.Duration) {
	t.Helper()
	if err := eng.PutWithTTL([]byte(key), []byte(value), ttl); err != nil {
		t.Fatalf("PutWithTTL(%q) failed: %v", key, err)
	}
}

// After churn (overwrites and deletes), Stats must account the dead
// entries: the overwritten old value, the deleted key's value, and its
// tombstone are all dead — no reader can reach them again. The byte
// view must agree with the file view: DataBytes is the sum of the data
// file sizes, DeadBytes is what a merge can reclaim, and DeadRatio is
// the dead share of the bytes on disk.
func TestStatsDeadAccountingAfterChurn(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k1", "value-1")
	mustPut(t, eng, "k2", "value-2")
	mustPut(t, eng, "k3", "value-3")
	if s := eng.Stats(); s.DeadEntries != 0 || s.DeadRatio != 0 {
		t.Fatalf("before churn: DeadEntries = %d, DeadRatio = %v, want 0/0", s.DeadEntries, s.DeadRatio)
	}
	mustPut(t, eng, "k1", "value-1-longer")          // overwrite: old value dies
	if err := eng.Delete([]byte("k2")); err != nil { // delete: value dies, tombstone born dead
		t.Fatalf("Delete(k2) failed: %v", err)
	}

	s := eng.Stats()
	if s.LiveEntries != 2 {
		t.Errorf("LiveEntries = %d, want 2 (k1, k3)", s.LiveEntries)
	}
	if want := uint64(3); s.DeadEntries != want {
		t.Errorf("DeadEntries = %d, want %d (overwritten value, deleted value, tombstone)", s.DeadEntries, want)
	}
	if len(s.Files) != 1 {
		t.Fatalf("Files has %d entries, want 1 (no rotation happened)", len(s.Files))
	}
	if want := uint64(5); s.Files[0].Entries != want {
		t.Errorf("Files[0].Entries = %d, want %d (3 puts + 1 overwrite + 1 tombstone)", s.Files[0].Entries, want)
	}
	if s.LiveBytes == 0 || s.LiveBytes >= s.DataBytes {
		t.Errorf("LiveBytes = %d, DataBytes = %d, want 0 < LiveBytes < DataBytes", s.LiveBytes, s.DataBytes)
	}
	if want := s.DataBytes - s.LiveBytes; s.DeadBytes != want {
		t.Errorf("DeadBytes = %d, want DataBytes-LiveBytes = %d", s.DeadBytes, want)
	}
	if want := float64(s.DeadBytes) / float64(s.DataBytes); s.DeadRatio != want {
		t.Errorf("DeadRatio = %v, want DeadBytes/DataBytes = %v", s.DeadRatio, want)
	}
	if s.DeadRatio <= 0 {
		t.Errorf("DeadRatio = %v, want > 0 after churn", s.DeadRatio)
	}
}

// The index lives entirely in memory, so Stats must publish a memory
// estimate that grows with the number of keys. It is an estimate — the
// assertion pins only that it is positive and monotone in the key set.
func TestStatsIndexMemoryEstimateGrowsWithKeys(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "a", "v")
	small := eng.Stats().IndexMemoryBytes
	if small == 0 {
		t.Fatal("IndexMemoryBytes = 0 with one live key, want a positive estimate")
	}
	for i := 0; i < 100; i++ {
		mustPut(t, eng, fmt.Sprintf("key-%02d", i), "v")
	}
	large := eng.Stats().IndexMemoryBytes
	if large <= small {
		t.Errorf("IndexMemoryBytes after 101 keys = %d, want > %d after 1 key", large, small)
	}
}

// Dead accounting must survive Close/Open: replay rebuilds the same
// live set, and the overwritten/deleted entries stay dead on disk, so
// a reopened engine reports the same dead-entry and byte observables.
func TestStatsDeadAccountingSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})

	mustPut(t, eng, "k1", "value-1")
	mustPut(t, eng, "k2", "value-2")
	mustPut(t, eng, "k1", "value-1-overwritten")
	if err := eng.Delete([]byte("k2")); err != nil {
		t.Fatalf("Delete(k2) failed: %v", err)
	}
	before := eng.Stats()
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, reopened)
	after := reopened.Stats()
	if after.LiveEntries != before.LiveEntries || after.DeadEntries != before.DeadEntries {
		t.Errorf("entries after reopen: live %d (was %d), dead %d (was %d)",
			after.LiveEntries, before.LiveEntries, after.DeadEntries, before.DeadEntries)
	}
	if after.LiveBytes != before.LiveBytes || after.DataBytes != before.DataBytes {
		t.Errorf("bytes after reopen: live %d (was %d), data %d (was %d)",
			after.LiveBytes, before.LiveBytes, after.DataBytes, before.DataBytes)
	}
}

// Scan is a public operation like the others, so Stats must count it —
// including scans that visit nothing.
func TestStatsCountsScans(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "k", "v")
	for i := 0; i < 3; i++ {
		if err := eng.Scan(engine.ScanOptions{Prefix: []byte("k")}, func(_, _ []byte) error { return nil }); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		if err := eng.Scan(engine.ScanOptions{Prefix: []byte("nope")}, func(_, _ []byte) error { return nil }); err != nil {
			t.Fatalf("empty Scan failed: %v", err)
		}
	}
	if got := eng.Stats().OpsScan; got != 6 {
		t.Errorf("OpsScan = %d, want 6 (hits and empty scans both count)", got)
	}
}

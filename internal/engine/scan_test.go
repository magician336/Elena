//go:build unix

package engine_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"elena/internal/engine"
)

// scanAll runs a Scan with opts and returns the visited keys in visit
// order plus a key→value map of what was visited.
func scanAll(t *testing.T, eng *engine.Engine, opts engine.ScanOptions) (keys []string, values map[string]string) {
	t.Helper()
	values = map[string]string{}
	if err := eng.Scan(opts, func(key, value []byte) error {
		keys = append(keys, string(key))
		values[string(key)] = string(value)
		return nil
	}); err != nil {
		t.Fatalf("Scan(%+v) failed: %v", opts, err)
	}
	return keys, values
}

// scanFixture writes the given keys (each mapped to "v-"+key) in the
// order given, deliberately not in sorted order.
func scanFixture(t *testing.T, eng *engine.Engine, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if err := eng.Put([]byte(key), []byte("v-"+key)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
	}
}

// Scan with zero options must visit every live key exactly once, in
// byte-lexicographic order regardless of write order — including the
// corners: empty key, keys that are prefixes of each other, and keys
// with high bytes. The byte order is what 0002 will encode against, so
// it must be deterministic and testable.
func TestScanYieldsAllKeysInByteOrder(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	keys := []string{"m-middle", "zeta", "alpha", "alpha\x00suffix", "", "\xffhigh", "a", "aa", "ab"}
	// Written in an order unrelated to byte order.
	scanFixture(t, eng, keys...)

	gotKeys, values := scanAll(t, eng, engine.ScanOptions{})
	wantKeys := slices.Clone(keys)
	slices.Sort(wantKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("Scan keys = %q, want byte-sorted %q", gotKeys, wantKeys)
	}
	for _, key := range wantKeys {
		if values[key] != "v-"+key {
			t.Errorf("Scan visited %q with value %q, want %q", key, values[key], "v-"+key)
		}
	}
}

// Scan of a fresh engine must visit nothing and report no error.
func TestScanEmptyEngineVisitsNothing(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	gotKeys, _ := scanAll(t, eng, engine.ScanOptions{})
	if len(gotKeys) != 0 {
		t.Errorf("Scan on empty engine visited %q, want nothing", gotKeys)
	}
}

// The Prefix option restricts Scan to keys starting with the given
// bytes; matching keys are still visited in byte order.
func TestScanPrefixFiltersKeys(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "ns2:cherry", "ns1:apple", "other", "ns1:banana", "ns1", "ns1x")

	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{"namespace prefix", "ns1:", []string{"ns1:apple", "ns1:banana"}},
		{"prefix without colon matches bare and extended", "ns1", []string{"ns1", "ns1:apple", "ns1:banana", "ns1x"}},
		{"empty prefix matches everything", "", []string{"ns1", "ns1:apple", "ns1:banana", "ns1x", "ns2:cherry", "other"}},
		{"no match", "zzz", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKeys, _ := scanAll(t, eng, engine.ScanOptions{Prefix: []byte(tt.prefix)})
			if !slices.Equal(gotKeys, tt.want) {
				t.Errorf("Scan(prefix %q) keys = %q, want %q", tt.prefix, gotKeys, tt.want)
			}
		})
	}
}

// The End option bounds the scan "up to some key": keys are yielded
// while key < End, or while key <= End when EndInclusive is set. End
// does not have to be an existing key, and it combines with Prefix so
// 0002 can encode a point-in-time read as a bounded prefix scan.
func TestScanEndBoundControlsInclusion(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "e", "a", "c", "b", "d")

	tests := []struct {
		name string
		opts engine.ScanOptions
		want []string
	}{
		{"end excluded by default", engine.ScanOptions{End: []byte("c")}, []string{"a", "b"}},
		{"end included on demand", engine.ScanOptions{End: []byte("c"), EndInclusive: true}, []string{"a", "b", "c"}},
		{"end between keys", engine.ScanOptions{End: []byte("bb")}, []string{"a", "b"}},
		{"end before everything yields nothing", engine.ScanOptions{End: []byte("a")}, nil},
		{"no end visits everything", engine.ScanOptions{}, []string{"a", "b", "c", "d", "e"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKeys, _ := scanAll(t, eng, tt.opts)
			if !slices.Equal(gotKeys, tt.want) {
				t.Errorf("Scan(%+v) keys = %q, want %q", tt.opts, gotKeys, tt.want)
			}
		})
	}
}

// End composes with Prefix: the scan stops where the bound says, and a
// bound before the prefix region simply yields nothing.
func TestScanEndBoundComposesWithPrefix(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "o:1", "n:3", "n:1", "n:2")

	tests := []struct {
		name string
		opts engine.ScanOptions
		want []string
	}{
		{"bounded prefix scan exclusive", engine.ScanOptions{Prefix: []byte("n:"), End: []byte("n:2")}, []string{"n:1"}},
		{"bounded prefix scan inclusive", engine.ScanOptions{Prefix: []byte("n:"), End: []byte("n:2"), EndInclusive: true}, []string{"n:1", "n:2"}},
		{"bound before prefix region", engine.ScanOptions{Prefix: []byte("n:"), End: []byte("a")}, nil},
		{"bound beyond all prefix keys", engine.ScanOptions{Prefix: []byte("n:"), End: []byte("zzz")}, []string{"n:1", "n:2", "n:3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKeys, _ := scanAll(t, eng, tt.opts)
			if !slices.Equal(gotKeys, tt.want) {
				t.Errorf("Scan(%+v) keys = %q, want %q", tt.opts, gotKeys, tt.want)
			}
		})
	}
}

// Scan must hide the engine's dead entries: a key deleted through the
// public API and a key whose TTL has elapsed are both absent from the
// scan, without the visitor needing to know either concept exists.
func TestScanSkipsExpiredAndDeletedEntries(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "keep-b", "dying", "keep-a", "gone", "keep-c")
	if err := eng.PutWithTTL([]byte("dying"), []byte("v-dying"), shortTTL); err != nil {
		t.Fatalf("PutWithTTL(dying) failed: %v", err)
	}
	if err := eng.Delete([]byte("gone")); err != nil {
		t.Fatalf("Delete(gone) failed: %v", err)
	}
	time.Sleep(expiryWait)

	gotKeys, values := scanAll(t, eng, engine.ScanOptions{})
	if want := []string{"keep-a", "keep-b", "keep-c"}; !slices.Equal(gotKeys, want) {
		t.Errorf("Scan keys = %q, want only live keys %q", gotKeys, want)
	}
	for _, key := range []string{"dying", "gone"} {
		if _, ok := values[key]; ok {
			t.Errorf("Scan visited dead key %q", key)
		}
	}
}

// A visit function returning an error must stop the scan at that item
// and surface the error to the caller unchanged, so callers can use it
// for early termination.
func TestScanVisitorErrorStopsIteration(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "a", "b", "c")

	sentinel := errors.New("stop at b")
	var visited []string
	err := eng.Scan(engine.ScanOptions{}, func(key, _ []byte) error {
		visited = append(visited, string(key))
		if string(key) == "b" {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Scan err = %v, want the visitor's sentinel %v", err, sentinel)
	}
	if want := []string{"a", "b"}; !slices.Equal(visited, want) {
		t.Errorf("visited keys = %q, want %q (scan must stop, not continue)", visited, want)
	}
}

// Scan's declared concurrency semantics are snapshot-style: the item
// set is fixed when Scan starts. Writes issued while the scan runs —
// from inside visit or from other goroutines — neither add nor remove
// items for the running scan: a newly Put key that sorts into the
// unvisited range does not appear, a Deleted key still appears with
// its snapshot value, and an overwritten key yields the value from the
// snapshot, not the overwrite.
func TestScanSnapshotSemanticsUnderWritesFromVisit(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "k1", "k2", "k3", "k4", "k5")

	var gotKeys []string
	values := map[string]string{}
	err := eng.Scan(engine.ScanOptions{}, func(key, value []byte) error {
		gotKeys = append(gotKeys, string(key))
		values[string(key)] = string(value)

		switch string(key) {
		case "k1":
			// Would sort between k2 and k3: must not appear later.
			if err := eng.Put([]byte("k2x"), []byte("intruder")); err != nil {
				t.Errorf("Put(k2x) from visit failed: %v", err)
			}
			// Would remove k3 from the index: must still appear later.
			if err := eng.Delete([]byte("k3")); err != nil {
				t.Errorf("Delete(k3) from visit failed: %v", err)
			}
			// Overwrites k4's value: the scan must still yield the
			// snapshot's original value.
			if err := eng.Put([]byte("k4"), []byte("overwritten")); err != nil {
				t.Errorf("Put(k4) from visit failed: %v", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if want := []string{"k1", "k2", "k3", "k4", "k5"}; !slices.Equal(gotKeys, want) {
		t.Errorf("Scan keys under concurrent writes = %q, want snapshot %q", gotKeys, want)
	}
	if got := values["k3"]; got != "v-k3" {
		t.Errorf("deleted-during-scan k3 yielded %q, want snapshot value %q", got, "v-k3")
	}
	if got := values["k4"]; got != "v-k4" {
		t.Errorf("overwritten-during-scan k4 yielded %q, want snapshot value %q", got, "v-k4")
	}

	// The writes did land for everyone else: a fresh scan sees them.
	afterKeys, afterValues := scanAll(t, eng, engine.ScanOptions{})
	if want := []string{"k1", "k2", "k2x", "k4", "k5"}; !slices.Equal(afterKeys, want) {
		t.Errorf("post-write Scan keys = %q, want %q", afterKeys, want)
	}
	if got := afterValues["k4"]; got != "overwritten" {
		t.Errorf("post-write Scan k4 = %q, want the overwrite", got)
	}
}

// A nil visit function must be rejected with an error instead of
// panicking on the first item.
func TestScanNilVisitorRejected(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	scanFixture(t, eng, "a")
	if err := eng.Scan(engine.ScanOptions{}, nil); err == nil {
		t.Error("Scan with nil visit must return an error")
	}
}

// Scans running concurrently with writers — puts, overwrites,
// deletes, TTL writes, and the rotations they induce — must never
// crash, never yield a half record, and must preserve the scan
// contract on every completed pass: strictly ascending keys and, for
// each visited key, the one canonical value that every version of that
// key carries. Run under -race this pins the snapshot design's
// thread-safety.
func TestScanConcurrentWithWritesAndRotation(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)

	// canonical returns the one value any write of key ever carries, so
	// any snapshot version a scan observes is assertable.
	canonical := func(key string) string { return "v:" + key }
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("seed%03d", i)
		if err := eng.Put([]byte(key), []byte(canonical(key))); err != nil {
			t.Fatalf("seed Put(%q) failed: %v", key, err)
		}
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				key := fmt.Sprintf("w%d-%04d", w, i%200)
				var err error
				switch i % 4 {
				case 0:
					err = eng.Put([]byte(key), []byte(canonical(key)))
				case 1:
					err = eng.PutWithTTL([]byte(key), []byte(canonical(key)), time.Hour)
				case 2:
					err = eng.PutWithTTL([]byte(key), []byte(canonical(key)), shortTTL)
				default:
					err = eng.Delete([]byte(key))
				}
				if err != nil && !errors.Is(err, engine.ErrClosed) {
					t.Errorf("writer %d op %d failed: %v", w, i, err)
					return
				}
			}
		}(w)
	}

	for pass := 0; pass < 20; pass++ {
		var keys []string
		err := eng.Scan(engine.ScanOptions{}, func(key, value []byte) error {
			if n := len(keys); n > 0 && string(key) <= keys[n-1] {
				t.Errorf("pass %d: scan order broke at %q after %q", pass, key, keys[n-1])
			}
			if string(value) != canonical(string(key)) {
				t.Errorf("pass %d: key %q yielded %q, want canonical %q", pass, key, value, canonical(string(key)))
			}
			keys = append(keys, string(key))
			return nil
		})
		if err != nil {
			t.Fatalf("Scan pass %d failed: %v", pass, err)
		}
	}

	close(stop)
	writers.Wait()

	// The engine must be fully intact afterwards: readable, scannable,
	// and closable without error.
	finalKeys, _ := scanAll(t, eng, engine.ScanOptions{})
	for _, key := range finalKeys {
		if _, err := eng.Get([]byte(key)); err != nil {
			t.Errorf("Get(%q) after concurrent phase failed: %v", key, err)
		}
	}
}

// Scan on a closed engine must fail with ErrClosed instead of touching
// released file handles.
func TestScanAfterCloseReturnsErrClosed(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	scanFixture(t, eng, "a")
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	err := eng.Scan(engine.ScanOptions{}, func(_, _ []byte) error { return nil })
	if !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Scan after Close err = %v, want ErrClosed", err)
	}
}

//go:build unix

package engine_test

import (
	"slices"
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

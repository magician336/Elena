//go:build unix

package engine_test

import (
	"bytes"
	"errors"
	"testing"

	"elena/internal/engine"
)

// The kill -9 scenarios prove the engine's headline claim — "an
// acknowledged write is durable" — under a real process crash: a child
// writer process keeps writing, the parent observes acks on stdout,
// sends SIGKILL at an arbitrary moment, and reopens the directory
// through the public API. Everything asserted here goes through the
// fault-injection seam (the child harness) and the main seam (Open,
// Get, Put); nothing inspects engine internals.

// A child writer keeps putting random key/value pairs (always sync
// policy). When the parent has observed the Nth ack on stdout it sends
// SIGKILL, then reopens the same directory: every acknowledged write
// must still be there with its exact value. The kill lands at an
// arbitrary point of the child's write loop, so writes may or may not
// have landed beyond the acks — but the acked prefix can never shrink.
func TestKill9AllAckedWritesSurvive(t *testing.T) {
	dir := t.TempDir()
	spec := WriterSpec{Dir: dir, Writes: 300, ValueSize: 64, Seed: 1}

	acked := KillWriterAfterAcks(t, spec, 120)
	if len(acked) < 120 {
		t.Fatalf("observed %d acks before the kill point, want at least 120", len(acked))
	}

	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	for _, w := range acked {
		got, err := eng.Get([]byte(w.Key))
		if err != nil {
			t.Fatalf("acked write %q lost after kill -9 and reopen: %v", w.Key, err)
		}
		if !bytes.Equal(got, w.Value) {
			t.Errorf("Get(%q) after kill -9 = %d bytes, want the exact acked %d bytes",
				w.Key, len(got), len(w.Value))
		}
	}
}

// The same claim must hold when the acked writes span several data
// files: the child simulates rotation rounds (close, duplicate the
// newest data file under the next ID, reopen — real rotation arrives in
// a later ticket), so recovery must replay several files in order and
// route reads across them.
func TestKill9AcrossDataFileRounds(t *testing.T) {
	dir := t.TempDir()
	spec := WriterSpec{Dir: dir, Writes: 300, ValueSize: 96, Seed: 2, RotateEvery: 40}

	acked := KillWriterAfterAcks(t, spec, 100)

	if got := countDataFiles(t, dir); got < 3 {
		t.Fatalf("fixture sanity: want at least 3 data files from rotation rounds at kill time, got %d", got)
	}

	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	for _, w := range acked {
		got, err := eng.Get([]byte(w.Key))
		if err != nil {
			t.Fatalf("acked write %q lost after kill -9 across file rounds: %v", w.Key, err)
		}
		if !bytes.Equal(got, w.Value) {
			t.Errorf("Get(%q) after kill -9 across file rounds = %d bytes, want the exact acked %d bytes",
				w.Key, len(got), len(w.Value))
		}
	}
}

// Killing at a point that is not aligned with an ack can leave a write
// half-appended (a torn tail). After reopen: every intended key is
// either fully present with its exact value or absent — never a
// partial or checksum-failing read — and the engine stays writable,
// with the post-kill writes surviving another reopen. That chain is
// what "the torn tail was truncated, not leaked" means through the
// public API.
func TestKill9MidWriteLeavesNoPartialRecord(t *testing.T) {
	dir := t.TempDir()
	// Large values slow each write+fsync down, raising the chance the
	// SIGKILL lands while put #2 is being appended; the exact landing
	// point is inherently racy, which is precisely what is under test.
	spec := WriterSpec{Dir: dir, Writes: 150, ValueSize: 32 << 10, Seed: 3}

	acked := KillWriterAfterAcks(t, spec, 1)

	eng := openEngine(t, dir)

	// Every key the child intended (acked or not) reads as exact value
	// or ErrNotFound; anything else means a partial record leaked.
	for _, w := range WriterDataset(spec) {
		got, err := eng.Get([]byte(w.Key))
		switch {
		case err == nil:
			if !bytes.Equal(got, w.Value) {
				t.Fatalf("Get(%q) after mid-write kill = %d bytes, want exact %d bytes or ErrNotFound",
					w.Key, len(got), len(w.Value))
			}
		case errors.Is(err, engine.ErrNotFound):
			// The write never fully happened — allowed for non-acked keys.
		default:
			t.Fatalf("Get(%q) after mid-write kill err = %v, want exact value or ErrNotFound", w.Key, err)
		}
	}

	// The first acked key must have survived.
	if len(acked) == 0 {
		t.Fatal("harness observed no acks before the kill")
	}
	first := acked[0]
	got, err := eng.Get([]byte(first.Key))
	if err != nil || !bytes.Equal(got, first.Value) {
		t.Fatalf("first acked write %q after mid-write kill = %d bytes, %v; want exact value, nil", first.Key, len(got), err)
	}

	// The truncated file must accept new writes, and the result must
	// survive yet another reopen — a stale torn tail would corrupt
	// every entry appended after it.
	if err := eng.Put([]byte("post-kill"), []byte("still-writable")); err != nil {
		t.Fatalf("Put after mid-write kill and reopen failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close after mid-write kill failed: %v", err)
	}

	final := openEngine(t, dir)
	defer closeEngine(t, final)
	if got, err := final.Get([]byte("post-kill")); err != nil || string(got) != "still-writable" {
		t.Errorf("Get(post-kill) after second reopen = %q, %v; want \"still-writable\", nil", got, err)
	}
	if got, err := final.Get([]byte(first.Key)); err != nil || !bytes.Equal(got, first.Value) {
		t.Errorf("Get(%q) after second reopen = %d bytes, %v; want exact acked value, nil", first.Key, len(got), err)
	}
}

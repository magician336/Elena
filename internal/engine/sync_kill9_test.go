//go:build unix

package engine_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/magician336/elena/internal/engine"
)

// Kill -9 under the non-always tiers. What these scenarios prove — and
// deliberately do not prove — about durability:
//
// SIGKILL kills the process, not the machine. The OS page cache
// survives, so writes acknowledged under interval/none still reach the
// disk eventually and the reopened directory still holds them. That is
// the process-crash axis, and it must hold for every tier: no exit-time
// code runs, no Close hook, nothing — recovery must handle whatever
// tail the kernel kept, and the acknowledged prefix can never shrink.
//
// What these tests do NOT claim is power-loss durability: under
// interval, at most one period of acknowledged writes can be lost to a
// machine fault; under none, the window is the whole page cache. Those
// windows are documented on engine.SyncPolicy and cannot be asserted
// with a process-crash harness — claiming they are zero would be
// fiction.

// A child writer runs under the interval tier with a short group-commit
// period and real rotations; the parent kills it mid-run. After
// reopen, every acknowledged write is present byte-exact — the process
// crash axis of the interval tier — and the intended-but-unacknowledged
// keys are fully present or fully absent, never torn.
func TestKill9IntervalTierAckedWritesSurviveProcessCrash(t *testing.T) {
	dir := t.TempDir()
	spec := WriterSpec{
		Dir: dir, Writes: 300, ValueSize: 64, Seed: 7,
		RotateEvery: 40, SyncPolicy: "interval", SyncPeriodMs: 2,
	}

	acked := KillWriterAfterAcks(t, spec, 120)

	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	for _, w := range acked {
		got, err := eng.Get([]byte(w.Key))
		if err != nil {
			t.Fatalf("acked write %q lost after interval-tier kill -9: %v", w.Key, err)
		}
		if !bytes.Equal(got, w.Value) {
			t.Errorf("Get(%q) after interval-tier kill -9 = %d bytes, want the exact acked %d bytes",
				w.Key, len(got), len(w.Value))
		}
	}
	// Torn-tail hygiene: every intended key reads as its exact value or
	// as ErrNotFound — a partial record must never leak, whatever the
	// tier of the writer that was killed.
	for _, w := range WriterDataset(spec) {
		got, err := eng.Get([]byte(w.Key))
		switch {
		case err == nil:
			if !bytes.Equal(got, w.Value) {
				t.Fatalf("Get(%q) after interval-tier kill = %d bytes, want exact value or ErrNotFound",
					w.Key, len(got))
			}
		case errors.Is(err, engine.ErrNotFound):
		default:
			t.Fatalf("Get(%q) after interval-tier kill err = %v, want hit or ErrNotFound", w.Key, err)
		}
	}
}

// The same process-crash axis under the none tier: the child never
// fdatasyncs anything, yet SIGKILL leaves the page cache alive, so the
// acknowledged prefix survives the reopen exactly. This pins the none
// tier's recovery integrity (torn-tail truncation, index rebuild, hint
// fallback) without pretending any power-loss durability.
func TestKill9NoneTierAckedWritesSurviveProcessCrash(t *testing.T) {
	dir := t.TempDir()
	spec := WriterSpec{
		Dir: dir, Writes: 200, ValueSize: 96, Seed: 8,
		RotateEvery: 50, SyncPolicy: "none",
	}

	acked := KillWriterAfterAcks(t, spec, 80)

	eng := openEngine(t, dir)
	defer closeEngine(t, eng)
	for _, w := range acked {
		got, err := eng.Get([]byte(w.Key))
		if err != nil {
			t.Fatalf("acked write %q lost after none-tier kill -9: %v", w.Key, err)
		}
		if !bytes.Equal(got, w.Value) {
			t.Errorf("Get(%q) after none-tier kill -9 = %d bytes, want the exact acked %d bytes",
				w.Key, len(got), len(w.Value))
		}
	}
}

package bench

import (
	"reflect"
	"testing"
)

// TestMicroRejectsInvalidArguments pins the workload's own argument
// validation: a workload with no operations or no bytes per value has
// no shape to time.
func TestMicroRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct {
		name      string
		count     int
		valueSize int
	}{
		{name: "zero count", count: 0, valueSize: 64},
		{name: "negative count", count: -1, valueSize: 64},
		{name: "zero value size", count: 10, valueSize: 0},
		{name: "negative value size", count: 10, valueSize: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Micro(tt.count, tt.valueSize); err == nil {
				t.Fatalf("Micro(%d, %d) succeeded, want error", tt.count, tt.valueSize)
			}
		})
	}
}

// TestMicroShape pins the micro workload's declared shape: one append
// phase writing count fresh keys with valueSize-byte values, then one
// read phase reading those keys back — the same shape the official
// bbolt bench tool drives with sequential write and read modes, so the
// methodology anchor compares like with like.
func TestMicroShape(t *testing.T) {
	const count, valueSize = 3, 64

	w, err := Micro(count, valueSize)
	if err != nil {
		t.Fatalf("Micro(%d, %d): %v", count, valueSize, err)
	}
	if w.Name != "micro" {
		t.Fatalf("workload name = %q, want %q", w.Name, "micro")
	}
	if len(w.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(w.Phases))
	}

	appendPhase, readPhase := w.Phases[0], w.Phases[1]
	if appendPhase.Name != "append" || readPhase.Name != "read" {
		t.Fatalf("phase names = %q, %q; want %q, %q",
			appendPhase.Name, readPhase.Name, "append", "read")
	}
	if len(appendPhase.Ops) != count || len(readPhase.Ops) != count {
		t.Fatalf("phase lengths = %d, %d; want %d, %d",
			len(appendPhase.Ops), len(readPhase.Ops), count, count)
	}
	for i := 0; i < count; i++ {
		op := appendPhase.Ops[i]
		if op.Kind != OpAppend {
			t.Fatalf("append phase op %d kind = %v, want OpAppend", i, op.Kind)
		}
		if op.Key != uint64(i) {
			t.Fatalf("append phase op %d key = %d, want %d", i, op.Key, i)
		}
		if len(op.Value) != valueSize {
			t.Fatalf("append phase op %d value length = %d, want %d", i, len(op.Value), valueSize)
		}

		rop := readPhase.Ops[i]
		if rop.Kind != OpRead {
			t.Fatalf("read phase op %d kind = %v, want OpRead", i, rop.Kind)
		}
		if rop.Key != uint64(i) {
			t.Fatalf("read phase op %d key = %d, want %d", i, rop.Key, i)
		}
	}
}

// TestMicroDeterministic pins reproducibility: the same arguments
// generate byte-identical operations every time, so two runs of the
// same workload differ only in what the engine under test does.
func TestMicroDeterministic(t *testing.T) {
	first, err := Micro(4, 32)
	if err != nil {
		t.Fatalf("Micro: %v", err)
	}
	second, err := Micro(4, 32)
	if err != nil {
		t.Fatalf("Micro: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Micro(4, 32) generated two different workloads")
	}
}

// TestMicroValuesDifferByKey pins that distinct keys carry distinct
// values: identical payloads would let a driver satisfy reads without
// touching the store.
func TestMicroValuesDifferByKey(t *testing.T) {
	w, err := Micro(3, 16)
	if err != nil {
		t.Fatalf("Micro: %v", err)
	}
	if reflect.DeepEqual(w.Phases[0].Ops[0].Value, w.Phases[0].Ops[1].Value) {
		t.Fatalf("keys 0 and 1 carry identical values")
	}
}

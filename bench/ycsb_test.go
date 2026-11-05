package bench

import (
	"reflect"
	"testing"
)

// ycsbSpecForTest is one small valid spec, the base the mutation tests
// below tweak one field at a time.
func ycsbSpecForTest() YCSBSpec {
	return YCSBSpec{
		Mix:       YCSBA,
		Keys:      16,
		Dist:      KeyUniform,
		RunOps:    40,
		ValueSize: 64,
		Seed:      7,
	}
}

// TestYCSBMixNamesAndRatios pins the three YCSB mixes the matrix runs:
// A's 50/50 read/update, B's 95/5, C's read-only — the ratios the
// generated run phase carries exactly.
func TestYCSBMixNamesAndRatios(t *testing.T) {
	tests := []struct {
		mix   YCSB
		name  string
		ratio float64
	}{
		{YCSBA, "ycsb-a", 0.5},
		{YCSBB, "ycsb-b", 0.95},
		{YCSBC, "ycsb-c", 1.0},
		{YCSB(99), "ycsb(99)", 0},
	}
	for _, tt := range tests {
		if got := tt.mix.String(); got != tt.name {
			t.Fatalf("YCSB(%d).String() = %q, want %q", int(tt.mix), got, tt.name)
		}
	}
	if r := YCSBA.ReadRatio(); r != 0.5 {
		t.Fatalf("YCSBA.ReadRatio() = %g, want 0.5", r)
	}
	if r := YCSBB.ReadRatio(); r != 0.95 {
		t.Fatalf("YCSBB.ReadRatio() = %g, want 0.95", r)
	}
	if r := YCSBC.ReadRatio(); r != 1.0 {
		t.Fatalf("YCSBC.ReadRatio() = %g, want 1.0", r)
	}
}

// TestYCSBWorkloadShape pins the two-phase structure: a load phase of
// exactly one append per key over the whole key space in key order,
// then a run phase of the declared mix — the YCSB separation of
// populating from measuring.
func TestYCSBWorkloadShape(t *testing.T) {
	w, err := YCSBWorkload(ycsbSpecForTest())
	if err != nil {
		t.Fatalf("YCSBWorkload: %v", err)
	}
	if w.Name != "ycsb-a" {
		t.Fatalf("workload name = %q, want ycsb-a", w.Name)
	}
	if len(w.Phases) != 2 || w.Phases[0].Name != "load" || w.Phases[1].Name != "run" {
		t.Fatalf("phases = %v, want [load run]", w.Phases)
	}

	load := w.Phases[0].Ops
	if len(load) != 16 {
		t.Fatalf("load ops = %d, want 16 (one per key)", len(load))
	}
	seen := map[uint64]bool{}
	for i, op := range load {
		if op.Kind != OpAppend {
			t.Fatalf("load op %d kind = %v, want append", i, op.Kind)
		}
		if op.Key != uint64(i) {
			t.Fatalf("load op %d key = %d, want %d (key order)", i, op.Key, i)
		}
		if len(op.Value) != 64 {
			t.Fatalf("load op %d value = %d bytes, want 64", i, len(op.Value))
		}
		seen[op.Key] = true
	}
	if len(seen) != 16 {
		t.Fatalf("load covered %d distinct keys, want 16", len(seen))
	}

	if got := len(w.Phases[1].Ops); got != 40 {
		t.Fatalf("run ops = %d, want 40", got)
	}
}

// TestYCSBWorkloadRunPhaseIsGeneratorSequence pins the delegation: the
// run phase is the seeded generator's own operation sequence, verbatim
// — no second sampling logic exists to drift from the generator's
// declared shape.
func TestYCSBWorkloadRunPhaseIsGeneratorSequence(t *testing.T) {
	spec := ycsbSpecForTest()
	w, err := YCSBWorkload(spec)
	if err != nil {
		t.Fatalf("YCSBWorkload: %v", err)
	}
	gen, err := Generate(GenSpec{
		Name: spec.Mix.String(), Ops: spec.RunOps, ReadRatio: spec.Mix.ReadRatio(),
		Keys: spec.Keys, Dist: spec.Dist, ZipfTheta: spec.ZipfTheta,
		ValueSize: spec.ValueSize, Seed: spec.Seed,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !reflect.DeepEqual(w.Phases[1].Ops, gen.Phases[0].Ops) {
		t.Fatal("run phase differs from the generator's sequence for the same spec")
	}
}

// TestYCSBWorkloadDeterministic pins the same-seed contract: one spec
// regenerates the identical two-phase workload, operation by
// operation, so every driver executes the same sequence.
func TestYCSBWorkloadDeterministic(t *testing.T) {
	first, err := YCSBWorkload(ycsbSpecForTest())
	if err != nil {
		t.Fatalf("YCSBWorkload: %v", err)
	}
	second, err := YCSBWorkload(ycsbSpecForTest())
	if err != nil {
		t.Fatalf("YCSBWorkload again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two generations of the same spec differ")
	}
}

// TestYCSBWorkloadSeedSelectsSequence pins that a different seed yields
// a different run sequence — the seed, and nothing else, selects the
// operation order.
func TestYCSBWorkloadSeedSelectsSequence(t *testing.T) {
	spec := ycsbSpecForTest()
	a, err := YCSBWorkload(spec)
	if err != nil {
		t.Fatalf("YCSBWorkload: %v", err)
	}
	spec.Seed = 8
	b, err := YCSBWorkload(spec)
	if err != nil {
		t.Fatalf("YCSBWorkload(seed 8): %v", err)
	}
	if reflect.DeepEqual(a.Phases[1].Ops, b.Phases[1].Ops) {
		t.Fatal("two seeds generated the identical run sequence")
	}
}

// TestYCSBWorkloadLoadAndUpdatePayloadsDiffer pins the payload
// discipline across phases: the load value of a key and any run-phase
// update of the same key never share bytes, so no update can be served
// as a duplicate of the load write.
func TestYCSBWorkloadLoadAndUpdatePayloadsDiffer(t *testing.T) {
	w, err := YCSBWorkload(ycsbSpecForTest())
	if err != nil {
		t.Fatalf("YCSBWorkload: %v", err)
	}
	compared := 0
	for _, op := range w.Phases[1].Ops {
		if op.Kind != OpAppend {
			continue
		}
		loadValue := w.Phases[0].Ops[op.Key].Value
		if string(loadValue) == string(op.Value) {
			t.Fatalf("run update of key %d reuses the load payload", op.Key)
		}
		compared++
	}
	if compared == 0 {
		t.Fatal("run phase carried no update to compare against the load payloads")
	}
}

// TestYCSBWorkloadRunMixExact pins the run phase's operation mix: reads
// are exactly round(RunOps*ratio) of the phase, for every mix — the
// declared shape and the generated shape are the same number.
func TestYCSBWorkloadRunMixExact(t *testing.T) {
	tests := []struct {
		mix  YCSB
		want int // expected reads out of 200 run ops
	}{
		{YCSBA, 100},
		{YCSBB, 190},
		{YCSBC, 200},
	}
	for _, tt := range tests {
		spec := ycsbSpecForTest()
		spec.Mix = tt.mix
		spec.RunOps = 200
		w, err := YCSBWorkload(spec)
		if err != nil {
			t.Fatalf("YCSBWorkload(%s): %v", tt.mix, err)
		}
		reads := 0
		for _, op := range w.Phases[1].Ops {
			if op.Kind == OpRead {
				reads++
			}
		}
		if reads != tt.want {
			t.Fatalf("%s run reads = %d, want %d", tt.mix, reads, tt.want)
		}
	}
}

// TestYCSBWorkloadInvalidSpec pins the validation surface: a bad field
// is an error, before any operation generates.
func TestYCSBWorkloadInvalidSpec(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*YCSBSpec)
	}{
		{"zero keys", func(s *YCSBSpec) { s.Keys = 0 }},
		{"zero run ops", func(s *YCSBSpec) { s.RunOps = 0 }},
		{"negative run ops", func(s *YCSBSpec) { s.RunOps = -1 }},
		{"zero value size", func(s *YCSBSpec) { s.ValueSize = 0 }},
		{"zipf without theta", func(s *YCSBSpec) { s.Dist = KeyZipf; s.ZipfTheta = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := ycsbSpecForTest()
			tt.mut(&spec)
			if _, err := YCSBWorkload(spec); err == nil {
				t.Fatal("YCSBWorkload succeeded on an invalid spec, want error")
			}
		})
	}
}

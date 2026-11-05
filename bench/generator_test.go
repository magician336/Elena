package bench

import (
	"math"
	"reflect"
	"testing"
)

// uniformSpec returns a valid uniform spec for one mixed phase, so
// table cases below vary only the field under test.
func uniformSpec() GenSpec {
	return GenSpec{
		Name:      "ycsb-a",
		Ops:       1000,
		ReadRatio: 0.5,
		Keys:      100,
		Dist:      KeyUniform,
		ValueSize: 64,
		Seed:      7,
	}
}

// TestGenerateRejectsInvalidSpecs pins the generator's own validation:
// a spec with no name, no operations, an impossible ratio, an empty key
// space, an undeclared distribution, a shapeless zipf, or no bytes per
// value declares no workload to build.
func TestGenerateRejectsInvalidSpecs(t *testing.T) {
	for _, tt := range []struct {
		name string
		mut  func(*GenSpec)
	}{
		{name: "empty name", mut: func(s *GenSpec) { s.Name = "" }},
		{name: "zero ops", mut: func(s *GenSpec) { s.Ops = 0 }},
		{name: "negative ops", mut: func(s *GenSpec) { s.Ops = -10 }},
		{name: "negative read ratio", mut: func(s *GenSpec) { s.ReadRatio = -0.01 }},
		{name: "read ratio above one", mut: func(s *GenSpec) { s.ReadRatio = 1.01 }},
		{name: "zero keys", mut: func(s *GenSpec) { s.Keys = 0 }},
		{name: "undeclared distribution", mut: func(s *GenSpec) { s.Dist = KeyDist(9) }},
		{name: "zipf without theta", mut: func(s *GenSpec) { s.Dist = KeyZipf; s.ZipfTheta = 0 }},
		{name: "zipf with negative theta", mut: func(s *GenSpec) { s.Dist = KeyZipf; s.ZipfTheta = -0.99 }},
		{name: "zero value size", mut: func(s *GenSpec) { s.ValueSize = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := uniformSpec()
			tt.mut(&spec)
			if _, err := Generate(spec); err == nil {
				t.Fatalf("Generate(%+v) succeeded, want error", spec)
			}
		})
	}
}

// TestGenerateShape pins the generated workload's declared shape: the
// spec's name names the workload, one mixed phase carries exactly Ops
// operations, and every key stays inside the declared space.
func TestGenerateShape(t *testing.T) {
	spec := uniformSpec()

	w, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if w.Name != spec.Name {
		t.Fatalf("workload name = %q, want %q", w.Name, spec.Name)
	}
	if len(w.Phases) != 1 {
		t.Fatalf("phases = %d, want 1", len(w.Phases))
	}
	ops := w.Phases[0].Ops
	if len(ops) != spec.Ops {
		t.Fatalf("phase length = %d, want %d", len(ops), spec.Ops)
	}
	for i, op := range ops {
		if op.Key >= spec.Keys {
			t.Fatalf("op %d key %d outside declared space [0, %d)", i, op.Key, spec.Keys)
		}
		if op.Kind == OpAppend && len(op.Value) != spec.ValueSize {
			t.Fatalf("append op %d value length = %d, want %d", i, len(op.Value), spec.ValueSize)
		}
	}
}

// TestGenerateDeterministic pins reproducibility: one seed builds the
// identical operation sequence every time — the property that makes a
// declared load repeatable across engines — while a different seed
// selects a different sequence.
func TestGenerateDeterministic(t *testing.T) {
	spec := uniformSpec()

	first, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed generated two different workloads")
	}

	other := spec
	other.Seed = 8
	third, err := Generate(other)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if reflect.DeepEqual(first, third) {
		t.Fatal("different seeds generated the same workload")
	}
}

// TestGenerateDeclaredReadWriteRatio pins the declared mix exactly: the
// generated phase carries precisely round(Ops*ReadRatio) reads and the
// rest appends — the measured ratio equals the declaration, not merely
// approaches it. Ratios cover the YCSB points (A 50/50, B 95/5, C all
// reads) and the pure-append Elena scene.
func TestGenerateDeclaredReadWriteRatio(t *testing.T) {
	for _, tt := range []struct {
		name      string
		ratio     float64
		ops       int
		wantReads int
	}{
		{name: "ycsb a half reads", ratio: 0.5, ops: 1000, wantReads: 500},
		{name: "ycsb b five percent writes", ratio: 0.95, ops: 1000, wantReads: 950},
		{name: "ycsb c all reads", ratio: 1.0, ops: 1000, wantReads: 1000},
		{name: "pure append", ratio: 0, ops: 1000, wantReads: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := uniformSpec()
			spec.ReadRatio = tt.ratio
			spec.Ops = tt.ops

			w, err := Generate(spec)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			reads := 0
			for _, op := range w.Phases[0].Ops {
				if op.Kind == OpRead {
					reads++
				}
			}
			if reads != tt.wantReads {
				t.Fatalf("reads = %d, want exactly %d for declared ratio %v", reads, tt.wantReads, tt.ratio)
			}
		})
	}
}

// TestGenerateInterleavesMix pins that the mix is really mixed: reads
// and appends alternate in one interleaved stream, not a block of
// reads followed by a block of appends — the shape that exercises an
// engine's read-write interleaving under one phase clock.
func TestGenerateInterleavesMix(t *testing.T) {
	spec := uniformSpec()

	w, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	runs := 1
	for i := 1; i < len(w.Phases[0].Ops); i++ {
		if w.Phases[0].Ops[i].Kind != w.Phases[0].Ops[i-1].Kind {
			runs++
		}
	}
	// A shuffled half-half mix of 1000 operations alternates on the
	// order of 500 times; an unshuffled one runs twice. Ten leaves the
	// pin far from either boundary.
	if runs < 10 {
		t.Fatalf("kind runs = %d, want an interleaved mix (>= 10 runs)", runs)
	}
}

// TestGenerateValues pins the value contract: reads carry no payload,
// and repeated writes of one key carry different payloads of the
// declared size — identical payloads would let an engine satisfy reads
// from a cached duplicate.
func TestGenerateValues(t *testing.T) {
	spec := uniformSpec()
	spec.Ops = 400
	spec.Keys = 5 // forces repeated writes of the same keys
	spec.ReadRatio = 0.5

	w, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := map[uint64][][]byte{}
	for _, op := range w.Phases[0].Ops {
		switch op.Kind {
		case OpRead:
			if op.Value != nil {
				t.Fatalf("read of key %d carries a value", op.Key)
			}
		case OpAppend:
			if len(op.Value) != spec.ValueSize {
				t.Fatalf("append of key %d value length = %d, want %d", op.Key, len(op.Value), spec.ValueSize)
			}
			for _, prev := range seen[op.Key] {
				if reflect.DeepEqual(prev, op.Value) {
					t.Fatalf("key %d appended an identical value twice", op.Key)
				}
			}
			seen[op.Key] = append(seen[op.Key], op.Value)
		}
	}
}

// TestGenerateUniformKeys pins the uniform declaration statistically:
// over many draws every key's observed frequency matches the uniform
// expectation within five sigmas of the binomial spread — the measured
// distribution equals the declared one.
func TestGenerateUniformKeys(t *testing.T) {
	const keys, ops = 50, 20000
	spec := uniformSpec()
	spec.Keys = keys
	spec.Ops = ops
	spec.ReadRatio = 0.5

	w, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	counts := make(map[uint64]int, keys)
	for _, op := range w.Phases[0].Ops {
		counts[op.Key]++
	}
	expect := float64(ops) / float64(keys) // uniform truth, independent of the sampler
	for key := uint64(0); key < keys; key++ {
		got := float64(counts[key])
		tol := math.Max(5, 5*math.Sqrt(expect*(1-1/float64(keys))))
		if math.Abs(got-expect) > tol {
			t.Fatalf("key %d observed %d times, want %v ± %.0f", key, counts[key], expect, tol)
		}
	}
}

// TestGenerateZipfKeys pins the zipf declaration statistically: every
// key's observed frequency matches its truncated-Zipf mass
// (k+1)^-theta / Z within five sigmas of the binomial spread. The
// masses are computed here from the distribution's definition, not
// from the sampler's internals.
func TestGenerateZipfKeys(t *testing.T) {
	const keys, ops, theta = 50, 20000, 0.99
	spec := uniformSpec()
	spec.Dist = KeyZipf
	spec.ZipfTheta = theta
	spec.Keys = keys
	spec.Ops = ops
	spec.ReadRatio = 0.5

	w, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	counts := make(map[uint64]int, keys)
	for _, op := range w.Phases[0].Ops {
		counts[op.Key]++
	}
	z := 0.0 // normalizer of the truncated Zipf, from the definition
	for k := 1; k <= keys; k++ {
		z += math.Pow(float64(k), -theta)
	}
	for key := uint64(0); key < keys; key++ {
		p := math.Pow(float64(key)+1, -theta) / z
		expect := float64(ops) * p
		tol := math.Max(5, 5*math.Sqrt(expect*(1-p)))
		got := float64(counts[key])
		if math.Abs(got-expect) > tol {
			t.Fatalf("key %d observed %d times, want %.1f ± %.1f", key, counts[key], expect, tol)
		}
	}
}

// TestGeneratedWorkloadRunsUnderHarness pins the composition seam: a
// generated load phase followed by a generated mixed phase runs
// through the same Driver lifecycle the micro workload uses — ticket
// 05's YCSB A/B/C and Elena scene loads assemble from exactly these
// pieces, in both key distributions.
func TestGeneratedWorkloadRunsUnderHarness(t *testing.T) {
	for _, tt := range []struct {
		name string
		dist KeyDist
	}{
		{name: "uniform", dist: KeyUniform},
		{name: "zipf", dist: KeyZipf},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loadSpec := uniformSpec()
			loadSpec.Name = tt.name + "-load"
			loadSpec.Dist = tt.dist
			loadSpec.ZipfTheta = 0.99
			loadSpec.ReadRatio = 0
			loadSpec.Ops = 1000
			loadSpec.Keys = 10

			runSpec := loadSpec
			runSpec.Name = tt.name + "-run"
			runSpec.ReadRatio = 0.5
			runSpec.Ops = 200

			load, err := Generate(loadSpec)
			if err != nil {
				t.Fatalf("Generate load: %v", err)
			}
			run, err := Generate(runSpec)
			if err != nil {
				t.Fatalf("Generate run: %v", err)
			}
			// 1000 uniform appends over 10 keys cover the space with
			// overwhelming margin, so every read of the mixed phase
			// resolves against an acknowledged append and the fake
			// driver's read guard proves the phases really ran in
			// order.
			w := Workload{Name: tt.name, Phases: append(load.Phases, run.Phases...)}

			drv := newFakeDriver()
			res, err := Run(drv, t.TempDir(), w)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(res.Phases) != 2 || res.Phases[0].Ops != 1000 || res.Phases[1].Ops != 200 {
				t.Fatalf("phase results = %+v, want load 1000 then run 200", res.Phases)
			}
			for _, pr := range res.Phases {
				if pr.Latency.P50 <= 0 || pr.Latency.P99 <= 0 || pr.Latency.P999 <= 0 {
					t.Fatalf("phase %q percentiles missing: %+v", pr.Phase, pr.Latency)
				}
			}
		})
	}
}

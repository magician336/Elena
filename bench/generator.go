package bench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

// KeyDist declares which distribution generated keys are drawn from.
type KeyDist int

const (
	// KeyUniform draws every key with equal probability over [0, Keys).
	KeyUniform KeyDist = iota

	// KeyZipf draws key k with probability proportional to
	// (k+1)^-ZipfTheta over [0, Keys) — the truncated Zipf of the YCSB
	// zipfian, theta 0.99 in YCSB's default.
	KeyZipf
)

// phaseMixed names the one phase a generated workload carries: reads
// and appends interleaved under the declared ratio.
const phaseMixed = "mixed"

// genStream is the generator's fixed PCG stream: seeds select
// sequences, the stream stays constant, so the seed alone identifies a
// generated workload.
const genStream = 0xB5AD4ECEDA1CE2A9

// GenSpec declares one generated workload: how many operations, the
// read share of the mix, the key space and its distribution, the bytes
// per appended value, and the seed. The generated phase carries
// precisely round(Ops*ReadRatio) reads and keys drawn from the declared
// distribution — declared shape and generated shape are the same
// number, pinned by statistical tests.
type GenSpec struct {
	// Name names the workload in reports; must not be empty.
	Name string

	// Ops is the number of operations to generate; must be positive.
	Ops int

	// ReadRatio is the declared read share of the mix, in [0, 1].
	ReadRatio float64

	// Keys is the size of the key space, drawn from [0, Keys); must be
	// positive.
	Keys uint64

	// Dist selects the key distribution over the key space.
	Dist KeyDist

	// ZipfTheta shapes KeyZipf: p(k) ∝ (k+1)^-theta. Required positive
	// when Dist is KeyZipf; ignored otherwise.
	ZipfTheta float64

	// ValueSize is the bytes per appended value; must be positive.
	ValueSize int

	// Seed selects the operation sequence: the same spec with the same
	// seed generates the identical workload, every time.
	Seed uint64
}

// Generate builds the spec's declared workload: one mixed phase of Ops
// operations whose read count is exactly the declared ratio and whose
// keys follow the declared distribution. Reads carry no value; each
// append's value derives from its key and that key's write ordinal, so
// repeated writes of one key never share a payload. Generation is pure:
// one seed, one sequence, no state between calls.
func Generate(spec GenSpec) (Workload, error) {
	if spec.Name == "" {
		return Workload{}, fmt.Errorf("generate workload: empty name")
	}
	if spec.Ops <= 0 {
		return Workload{}, fmt.Errorf("generate workload %q: ops %d must be positive", spec.Name, spec.Ops)
	}
	if spec.ReadRatio < 0 || spec.ReadRatio > 1 {
		return Workload{}, fmt.Errorf("generate workload %q: read ratio %g out of [0, 1]", spec.Name, spec.ReadRatio)
	}
	if spec.Keys == 0 {
		return Workload{}, fmt.Errorf("generate workload %q: keys must be positive", spec.Name)
	}
	if spec.ValueSize <= 0 {
		return Workload{}, fmt.Errorf("generate workload %q: value size %d must be positive", spec.Name, spec.ValueSize)
	}
	sampleKey, err := newKeySampler(spec)
	if err != nil {
		return Workload{}, err
	}

	reads := int(math.Round(float64(spec.Ops) * spec.ReadRatio))
	kinds := make([]OpKind, spec.Ops)
	for i := range kinds {
		if i < reads {
			kinds[i] = OpRead
		} else {
			kinds[i] = OpAppend
		}
	}
	r := rand.New(rand.NewPCG(spec.Seed, genStream))
	r.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })

	ordinals := make(map[uint64]int, int(spec.Keys))
	ops := make([]Op, spec.Ops)
	for i, kind := range kinds {
		key := sampleKey(r)
		op := Op{Kind: kind, Key: key}
		if kind == OpAppend {
			op.Value = genValue(key, ordinals[key], spec.ValueSize)
			ordinals[key]++
		}
		ops[i] = op
	}
	return Workload{Name: spec.Name, Phases: []Phase{{Name: phaseMixed, Ops: ops}}}, nil
}

// newKeySampler returns the spec's key-drawing function.
func newKeySampler(spec GenSpec) (func(*rand.Rand) uint64, error) {
	switch spec.Dist {
	case KeyUniform:
		return func(r *rand.Rand) uint64 { return r.Uint64N(spec.Keys) }, nil
	case KeyZipf:
		if spec.ZipfTheta <= 0 {
			return nil, fmt.Errorf("generate workload %q: zipf theta %g must be positive", spec.Name, spec.ZipfTheta)
		}
		cdf := zipfCDF(spec.Keys, spec.ZipfTheta)
		return func(r *rand.Rand) uint64 {
			u := r.Float64()
			return uint64(sort.SearchFloat64s(cdf, u))
		}, nil
	default:
		return nil, fmt.Errorf("generate workload %q: unknown key distribution %d", spec.Name, int(spec.Dist))
	}
}

// zipfCDF precomputes the cumulative masses of the truncated Zipf over
// [0, keys): p(k) ∝ (k+1)^-theta, normalized so the table ends at
// exactly 1. Drawing a key is one uniform draw followed by one binary
// search — the sampled frequency equals the declared mass by
// construction, which the statistical pins check. The table costs
// O(keys) floats for the lifetime of one Generate call.
func zipfCDF(keys uint64, theta float64) []float64 {
	cdf := make([]float64, keys)
	z := 0.0
	for k := uint64(0); k < keys; k++ {
		z += math.Pow(float64(k)+1, -theta)
		cdf[k] = z
	}
	for i := range cdf {
		cdf[i] /= z
	}
	return cdf
}

// genValue derives one deterministic value from its key and that key's
// write ordinal: the same arguments regenerate identical bytes, and
// different arguments differ — repeated writes of one key carry
// different payloads, so no engine can satisfy a read from a cached
// duplicate of an earlier write.
func genValue(key uint64, ordinal, size int) []byte {
	value := make([]byte, size)
	state := key*0x9E3779B97F4A7C15 + uint64(ordinal)*0xBF58476D1CE4E5B9 + 1
	for i := range value {
		state = state*6364136223846793005 + 1442695040888963407
		value[i] = byte(state >> 56)
	}
	return value
}

package bench

import "fmt"

// YCSB names one of the three YCSB core workload mixes the comparison
// matrix runs. A mix declares the run phase's read share; the writes
// are updates of existing keys.
type YCSB int

const (
	// YCSBA is the read/update mix at 50/50.
	YCSBA YCSB = iota

	// YCSBB is the read/update mix at 95/5.
	YCSBB

	// YCSBC is read-only.
	YCSBC
)

// String returns the mix's report name.
func (y YCSB) String() string {
	switch y {
	case YCSBA:
		return "ycsb-a"
	case YCSBB:
		return "ycsb-b"
	case YCSBC:
		return "ycsb-c"
	default:
		return fmt.Sprintf("ycsb(%d)", int(y))
	}
}

// ReadRatio returns the mix's declared read share of the run phase.
func (y YCSB) ReadRatio() float64 {
	switch y {
	case YCSBA:
		return 0.5
	case YCSBB:
		return 0.95
	case YCSBC:
		return 1.0
	default:
		return 0
	}
}

// phaseLoad and phaseRun name the two phases a YCSB workload carries:
// the load phase populates the key space, the run phase measures the
// declared mix over it.
const (
	phaseLoad = "load"
	phaseRun  = "run"
)

// loadOrdinal is the write ordinal the load phase derives its values
// from. The run phase counts ordinals from zero, so the load value of a
// key and every run-phase update of it never share a payload — no
// engine can serve a verified read from a duplicate of another write.
const loadOrdinal = -1

// YCSBSpec declares one matrix cell's workload: the mix, the key space
// and its distribution, the run phase's operation count, the bytes per
// value, and the seed. Every driver the matrix runs over one spec
// executes the identical two-phase sequence the seed selects.
type YCSBSpec struct {
	// Mix selects the run phase's read/update ratio.
	Mix YCSB

	// Keys is the size of the key space, drawn from [0, Keys); the
	// load phase appends exactly one value per key. Must be positive.
	Keys uint64

	// Dist selects the run phase's key distribution over the key
	// space; the load phase always walks keys in key order.
	Dist KeyDist

	// ZipfTheta shapes Dist KeyZipf; ignored otherwise.
	ZipfTheta float64

	// RunOps is the run phase's operation count; must be positive.
	RunOps int

	// ValueSize is the bytes per appended value; must be positive.
	ValueSize int

	// Seed selects the run phase's operation sequence.
	Seed uint64
}

// YCSBWorkload builds the spec's two-phase workload: a load phase of
// one append per key in key order — the YCSB load step that populates
// the key space — then a run phase carrying the mix's declared
// read/update ratio over the same keys. The run phase is the seeded
// generator's own sequence, verbatim: one sampling logic exists, so the
// same spec drives every driver through the identical operations. Load
// values derive from the key and a load-only write ordinal, run-phase
// updates from key and per-key run ordinal, so a load value and an
// update never share a payload.
func YCSBWorkload(spec YCSBSpec) (Workload, error) {
	if spec.Keys == 0 {
		return Workload{}, fmt.Errorf("ycsb workload %s: keys must be positive", spec.Mix)
	}
	if spec.RunOps <= 0 {
		return Workload{}, fmt.Errorf("ycsb workload %s: run ops %d must be positive", spec.Mix, spec.RunOps)
	}
	if spec.ValueSize <= 0 {
		return Workload{}, fmt.Errorf("ycsb workload %s: value size %d must be positive", spec.Mix, spec.ValueSize)
	}
	run, err := Generate(GenSpec{
		Name: spec.Mix.String(), Ops: spec.RunOps, ReadRatio: spec.Mix.ReadRatio(),
		Keys: spec.Keys, Dist: spec.Dist, ZipfTheta: spec.ZipfTheta,
		ValueSize: spec.ValueSize, Seed: spec.Seed,
	})
	if err != nil {
		return Workload{}, err
	}

	loadOps := make([]Op, spec.Keys)
	for k := range loadOps {
		loadOps[k] = Op{Kind: OpAppend, Key: uint64(k), Value: genValue(uint64(k), loadOrdinal, spec.ValueSize)}
	}
	return Workload{
		Name: spec.Mix.String(),
		Phases: []Phase{
			{Name: phaseLoad, Ops: loadOps},
			{Name: phaseRun, Ops: run.Phases[0].Ops},
		},
	}, nil
}

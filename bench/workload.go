package bench

import "fmt"

// OpKind distinguishes the two operation classes the harness drives.
type OpKind int

const (
	// OpAppend adds one value under one key: for the elena driver the
	// key is a trajectory and the value its next event; for KV drivers
	// it is a plain key-value put.
	OpAppend OpKind = iota

	// OpRead reads the latest appended value of one key back.
	OpRead
)

// String returns the operation's short name.
func (k OpKind) String() string {
	switch k {
	case OpAppend:
		return "append"
	case OpRead:
		return "read"
	default:
		return fmt.Sprintf("opkind(%d)", int(k))
	}
}

// Op is one operation of a workload phase. Keys are logical: drivers
// map them onto their engine's own addressing, and a read of key k
// must return what the last append of key k wrote.
type Op struct {
	Kind OpKind
	Key  uint64
	// Value is the bytes to append; reads ignore it.
	Value []byte
}

// Phase is one timed stretch of a workload: a name for reporting and
// the operations to drive in order.
type Phase struct {
	Name string
	Ops  []Op
}

// Workload is a named sequence of phases for one driver.
type Workload struct {
	Name   string
	Phases []Phase
}

// Micro builds the micro workload: one append phase writing count
// fresh keys, each with a valueSize-byte value, then one read phase
// reading those keys back, both in key order. The shape mirrors the
// official bbolt bench tool's sequential write and read modes over
// count items — the workload the methodology anchor compares against.
// Values derive deterministically from the key and position, so the
// same arguments always generate the identical workload. Both
// arguments must be positive.
func Micro(count, valueSize int) (Workload, error) {
	if count <= 0 {
		return Workload{}, fmt.Errorf("micro workload: count %d must be positive", count)
	}
	if valueSize <= 0 {
		return Workload{}, fmt.Errorf("micro workload: value size %d must be positive", valueSize)
	}
	appendOps := make([]Op, count)
	readOps := make([]Op, count)
	for i := 0; i < count; i++ {
		appendOps[i] = Op{Kind: OpAppend, Key: uint64(i), Value: microValue(uint64(i), valueSize)}
		readOps[i] = Op{Kind: OpRead, Key: uint64(i)}
	}
	return Workload{
		Name: "micro",
		Phases: []Phase{
			{Name: "append", Ops: appendOps},
			{Name: "read", Ops: readOps},
		},
	}, nil
}

// microValue derives one deterministic value from its key: a byte
// stream that differs per key yet regenerates identically on every
// call, without holding any state between workloads.
func microValue(key uint64, size int) []byte {
	value := make([]byte, size)
	state := key*0x9E3779B97F4A7C15 + 1
	for i := range value {
		state = state*6364136223846793005 + 1442695040888963407
		value[i] = byte(state >> 56)
	}
	return value
}

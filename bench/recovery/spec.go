// Package recovery measures how long the elena store takes to come
// back after a kill -9: a child process appends a declared dataset
// through the public library API and acknowledges every event, the
// parent observes the acknowledgments, kills the child with SIGKILL,
// and times the reopen — kill to Open returning to the first read.
// The rig is the recovery-time instrument of the bench module; it adds
// no timing API to the engine or the library, it only watches the
// public surface from outside the process under fault.
package recovery

import (
	"fmt"
	"strings"
)

// Spec configures one rig run: the dataset the child appends and the
// point the parent kills at.
type Spec struct {
	// Dir is the store directory the child opens; it must not hold a
	// store from an earlier run.
	Dir string

	// Sessions is the number of sessions the child creates; schedule
	// event e belongs to session e%Sessions as that session's
	// (e/Sessions + 1)-th event, so sequence numbers derive from the
	// schedule position alone.
	Sessions int

	// Events is the schedule size: the total number of appends.
	Events int

	// PayloadSize is the exact payload length of every event, in
	// bytes; Events times PayloadSize is the tier's dataset size.
	PayloadSize int

	// Seed selects the deterministic event payloads.
	Seed int64

	// KillAfterAcks selects the kill point: 0 kills once the whole
	// schedule is acknowledged and the child holds, n > 0 kills after
	// the n-th acknowledgment line — a mid-run kill. It must be 0 or
	// lie within [1, Events].
	KillAfterAcks int
}

// Validate rejects specs the child or the parent cannot run with.
func (s Spec) Validate() error {
	switch {
	case s.Dir == "":
		return fmt.Errorf("recovery spec without a directory: %+v", s)
	case s.Sessions <= 0:
		return fmt.Errorf("recovery spec without sessions: %+v", s)
	case s.Events <= 0:
		return fmt.Errorf("recovery spec without events: %+v", s)
	case s.PayloadSize <= 0:
		return fmt.Errorf("recovery spec without a payload size: %+v", s)
	case s.KillAfterAcks < 0 || s.KillAfterAcks > s.Events:
		return fmt.Errorf("recovery spec KillAfterAcks %d outside [0, %d]: %+v",
			s.KillAfterAcks, s.Events, s)
	default:
		return nil
	}
}

// TargetBytes returns the dataset size the spec writes: events times
// payload bytes. Tiers are named after this number.
func (s Spec) TargetBytes() int64 {
	return int64(s.Events) * int64(s.PayloadSize)
}

// Tier couples one reportable data-size tier with its default value
// size and session count; the tiers are the scale axis of the
// recovery-time report.
type Tier struct {
	// Name is the tier's report name.
	Name string

	// TargetBytes is the dataset size the tier writes.
	TargetBytes int64

	// ValueSize is the payload length every event carries.
	ValueSize int

	// Sessions is the session count of the tier's schedule.
	Sessions int
}

// tiers is the tier table, in report order. The micro tier is sized
// for a seconds-scale run — it is the CI smoke shape; the other tiers
// are manual, declared-environment runs and take as long as their
// append schedule under the always-sync regime.
var tiers = []Tier{
	{Name: "micro", TargetBytes: 64 << 10, ValueSize: 512, Sessions: 4},
	{Name: "256mb", TargetBytes: 256 << 20, ValueSize: 4096, Sessions: 8},
	{Name: "1gb", TargetBytes: 1 << 30, ValueSize: 4096, Sessions: 8},
	{Name: "4gb", TargetBytes: 4 << 30, ValueSize: 4096, Sessions: 8},
}

// TierNames returns the tier names in report order.
func TierNames() []string {
	names := make([]string, len(tiers))
	for i, tier := range tiers {
		names[i] = tier.Name
	}
	return names
}

// TierByName returns the tier under its report name. An unknown name
// is an error listing the known tiers, not a default.
func TierByName(name string) (Tier, error) {
	for _, tier := range tiers {
		if tier.Name == name {
			return tier, nil
		}
	}
	return Tier{}, fmt.Errorf("unknown recovery tier %q, known tiers: %v",
		name, strings.Join(TierNames(), ", "))
}

// Spec derives the runnable spec of one tier run: events come off
// target size and value size, the directory and seed carry the run's
// identity, and the kill point passes through — 0 for the timed
// hold-kill, n > 0 for a mid-run kill.
func (t Tier) Spec(dir string, seed int64, killAfterAcks int) Spec {
	return Spec{
		Dir:           dir,
		Sessions:      t.Sessions,
		Events:        int(t.TargetBytes / int64(t.ValueSize)),
		PayloadSize:   t.ValueSize,
		Seed:          seed,
		KillAfterAcks: killAfterAcks,
	}
}

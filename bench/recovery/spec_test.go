package recovery

import (
	"strings"
	"testing"
)

// validSpec returns a valid Spec so table cases below vary only the
// field under test.
func validSpec() Spec {
	return Spec{
		Dir:         "/tmp/rig-store",
		Sessions:    4,
		Events:      64,
		PayloadSize: 512,
		Seed:        1,
	}
}

// TestSpecValidate pins which specs the rig accepts: the kill point is
// either the hold marker (0) or an ack count inside the schedule.
func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Spec)
		want string
	}{
		{"valid without a directory", func(s *Spec) { s.Dir = "" }, "directory"},
		{"valid without sessions", func(s *Spec) { s.Sessions = 0 }, "sessions"},
		{"valid without events", func(s *Spec) { s.Events = 0 }, "events"},
		{"valid without a payload size", func(s *Spec) { s.PayloadSize = 0 }, "payload size"},
		{"kill after a negative ack count", func(s *Spec) { s.KillAfterAcks = -1 }, "KillAfterAcks"},
		{"kill beyond the schedule", func(s *Spec) { s.KillAfterAcks = 65 }, "KillAfterAcks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mut(&spec)
			err := spec.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() of %+v = %v, want an error mentioning %q", spec, err, tc.want)
			}
		})
	}
	for _, acks := range []int{0, 1, 64} {
		spec := validSpec()
		spec.KillAfterAcks = acks
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate() with KillAfterAcks=%d = %v, want nil", acks, err)
		}
	}
}

// TestSpecTargetBytes pins the dataset-size derivation the tiers are
// named after: events times payload bytes.
func TestSpecTargetBytes(t *testing.T) {
	spec := validSpec() // 64 events x 512 B
	if got := spec.TargetBytes(); got != 64*512 {
		t.Fatalf("TargetBytes() = %d, want %d", got, 64*512)
	}
}

// TestTierTable pins the tier table: the names the CLI accepts, the
// target sizes they stand for, and that each target divides exactly
// into whole events at the tier's value size.
func TestTierTable(t *testing.T) {
	want := map[string]int64{
		"micro": 64 << 10,
		"256mb": 256 << 20,
		"1gb":   1 << 30,
		"4gb":   4 << 30,
	}
	names := TierNames()
	if len(names) != len(want) {
		t.Fatalf("TierNames() = %v, want %d tiers", names, len(want))
	}
	for _, name := range names {
		target, ok := want[name]
		if !ok {
			t.Fatalf("TierNames() holds unexpected tier %q", name)
		}
		tier, err := TierByName(name)
		if err != nil {
			t.Fatalf("TierByName(%q) = %v", name, err)
		}
		if tier.TargetBytes != target {
			t.Fatalf("tier %q targets %d bytes, want %d", name, tier.TargetBytes, target)
		}
		if tier.TargetBytes%int64(tier.ValueSize) != 0 {
			t.Fatalf("tier %q target %d does not divide into whole events at %dB values",
				name, tier.TargetBytes, tier.ValueSize)
		}
		if tier.Sessions <= 0 {
			t.Fatalf("tier %q has %d sessions", name, tier.Sessions)
		}
	}
}

// TestTierByName pins the unknown-name error: an unknown tier is a
// caller error listing the known names, not a default.
func TestTierByName(t *testing.T) {
	_, err := TierByName("1tb")
	if err == nil || !strings.Contains(err.Error(), "micro") || !strings.Contains(err.Error(), "4gb") {
		t.Fatalf("TierByName(%q) = %v, want an error listing the known tiers", "1tb", err)
	}
}

// TestTierSpec pins the derivation from one tier to a runnable spec:
// events come off target and value size, the directory and seed carry
// the run's identity, and the kill point passes through untouched.
func TestTierSpec(t *testing.T) {
	tier, err := TierByName("256mb")
	if err != nil {
		t.Fatalf("TierByName(256mb) = %v", err)
	}
	spec := tier.Spec("/data/rig/256mb-run1", 7, 0)
	if spec.Dir != "/data/rig/256mb-run1" || spec.Seed != 7 || spec.KillAfterAcks != 0 {
		t.Fatalf("tier.Spec carried %+v, want the given dir, seed, and kill point", spec)
	}
	if spec.Events != 256<<20/4096 || spec.PayloadSize != 4096 || spec.Sessions != tier.Sessions {
		t.Fatalf("tier.Spec derived %d events x %dB over %d sessions, want %d x %dB over %d",
			spec.Events, spec.PayloadSize, spec.Sessions, 256<<20/4096, 4096, tier.Sessions)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("derived spec does not validate: %v", err)
	}
}

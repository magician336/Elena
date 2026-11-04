package bench

import (
	"strings"
	"testing"
	"time"

	"elena"
)

// TestRunAppendOnly drives the pure-append scenario load at smoke size
// against a real store directory and pins the result's shape: one
// append-only phase over every session-round event, attributed to the
// elena driver.
func TestRunAppendOnly(t *testing.T) {
	dir := t.TempDir()
	res, err := RunAppendOnly(dir, AppendOnlySpec{Sessions: 3, EventsPerSession: 4, ValueSize: 64})
	if err != nil {
		t.Fatalf("RunAppendOnly: %v", err)
	}
	if res.Driver != "elena" {
		t.Fatalf("Driver = %q, want elena", res.Driver)
	}
	if res.Workload != "elena-append-only" {
		t.Fatalf("Workload = %q, want elena-append-only", res.Workload)
	}
	if len(res.Phases) != 1 {
		t.Fatalf("phases = %d, want 1", len(res.Phases))
	}
	pr := res.Phases[0]
	if pr.Phase != "append-only" {
		t.Fatalf("phase name = %q, want append-only", pr.Phase)
	}
	if pr.Ops != 12 {
		t.Fatalf("phase ops = %d, want 12", pr.Ops)
	}
	if pr.Throughput <= 0 {
		t.Fatalf("throughput = %g, want positive", pr.Throughput)
	}
	if pr.Latency.Count != 12 {
		t.Fatalf("latency count = %d, want 12", pr.Latency.Count)
	}

	// The store directory is real: reopening it shows every session's
	// whole trajectory on disk, not just in memory.
	st, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("reopen %s: %v", dir, err)
	}
	defer func() { _ = st.Close() }()
	for s := 0; s < 3; s++ {
		sess, err := st.CreateSession(elenaNamespace, scenarioSessionName("append", s))
		if err != nil {
			t.Fatalf("CreateSession %d: %v", s, err)
		}
		events, err := sess.Events()
		if err != nil {
			t.Fatalf("Events %d: %v", s, err)
		}
		if len(events) != 4 {
			t.Fatalf("session %d holds %d events after reopen, want 4", s, len(events))
		}
	}
}

// TestRunAppendOnlyRejectsInvalidSpec pins the declared-shape contract:
// every field must be positive, and a bad one fails the run before any
// store is opened.
func TestRunAppendOnlyRejectsInvalidSpec(t *testing.T) {
	cases := []struct {
		name string
		spec AppendOnlySpec
	}{
		{"zero sessions", AppendOnlySpec{Sessions: 0, EventsPerSession: 1, ValueSize: 1}},
		{"negative sessions", AppendOnlySpec{Sessions: -1, EventsPerSession: 1, ValueSize: 1}},
		{"zero events", AppendOnlySpec{Sessions: 1, EventsPerSession: 0, ValueSize: 1}},
		{"zero value size", AppendOnlySpec{Sessions: 1, EventsPerSession: 1, ValueSize: 0}},
	}
	for _, tc := range cases {
		if _, err := RunAppendOnly(t.TempDir(), tc.spec); err == nil {
			t.Errorf("%s: RunAppendOnly succeeded, want failure", tc.name)
		}
	}
}

// TestRunAppendOnlyRenders pins that the scenario's Result drops
// straight into the shared report rendering.
func TestRunAppendOnlyRenders(t *testing.T) {
	res, err := RunAppendOnly(t.TempDir(), AppendOnlySpec{Sessions: 1, EventsPerSession: 1, ValueSize: 32})
	if err != nil {
		t.Fatalf("RunAppendOnly: %v", err)
	}
	md := RenderMarkdown([]Result{res})
	for _, want := range []string{"elena", "elena-append-only", "append-only"} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered report missing %q:\n%s", want, md)
		}
	}
}

// TestRunPointInTime drives the append + point-in-time-read scenario
// load at smoke size and pins the shape: one mixed phase over every
// append and every read.
func TestRunPointInTime(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPointInTime(dir, PointInTimeSpec{
		Sessions: 2, EventsPerSession: 3, ReadsPerSession: 2,
		ValueSize: 48, Seed: 7,
	})
	if err != nil {
		t.Fatalf("RunPointInTime: %v", err)
	}
	if res.Driver != "elena" || res.Workload != "elena-point-in-time" {
		t.Fatalf("driver/workload = %q/%q, want elena/elena-point-in-time", res.Driver, res.Workload)
	}
	if len(res.Phases) != 1 || res.Phases[0].Phase != "point-in-time" {
		t.Fatalf("phases = %+v, want one named point-in-time", res.Phases)
	}
	pr := res.Phases[0]
	if want := 2 * (3 + 2); pr.Ops != want {
		t.Fatalf("phase ops = %d, want %d", pr.Ops, want)
	}
	if pr.Latency.Count != 10 {
		t.Fatalf("latency count = %d, want 10", pr.Latency.Count)
	}
	if pr.Throughput <= 0 {
		t.Fatalf("throughput = %g, want positive", pr.Throughput)
	}

	// Every read verified its point in time against the payloads the
	// appends wrote, so a completed run already proves the mix correct;
	// reopen adds the on-disk half.
	st, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st.Close() }()
	for s := 0; s < 2; s++ {
		sess, err := st.CreateSession(elenaNamespace, scenarioSessionName("point", s))
		if err != nil {
			t.Fatalf("CreateSession %d: %v", s, err)
		}
		events, err := sess.Events()
		if err != nil {
			t.Fatalf("Events %d: %v", s, err)
		}
		if len(events) != 3 {
			t.Fatalf("session %d holds %d events after reopen, want 3", s, len(events))
		}
	}
}

// TestPointInTimeOpsDeterministic pins the seeded operation stream: the
// same seed generates the identical plan, and every session's first
// operation is an append — no read runs before its trajectory exists.
func TestPointInTimeOpsDeterministic(t *testing.T) {
	spec := PointInTimeSpec{Sessions: 3, EventsPerSession: 4, ReadsPerSession: 3, ValueSize: 8, Seed: 42}
	first, err := pointInTimeOps(spec)
	if err != nil {
		t.Fatalf("pointInTimeOps: %v", err)
	}
	second, err := pointInTimeOps(spec)
	if err != nil {
		t.Fatalf("pointInTimeOps again: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("plan session counts differ: %d vs %d", len(first), len(second))
	}
	for s := range first {
		if len(first[s]) == 0 {
			t.Fatalf("session %d plan is empty", s)
		}
		if first[s][0].read {
			t.Fatalf("session %d first op is a read, want an append", s)
		}
		if len(first[s]) != len(second[s]) {
			t.Fatalf("session %d plan lengths differ: %d vs %d", s, len(first[s]), len(second[s]))
		}
		for i := range first[s] {
			if first[s][i] != second[s][i] {
				t.Fatalf("session %d op %d differs across runs: %+v vs %+v", s, i, first[s][i], second[s][i])
			}
		}
	}
}

// TestPointInTimeOpsReadBounds pins that every planned read's sequence
// number stays at or under the appends its session has already
// planned — the bound the run's full-trajectory verification relies on.
func TestPointInTimeOpsReadBounds(t *testing.T) {
	for _, seed := range []uint64{0, 1, 9, 42, 1 << 40} {
		plan, err := pointInTimeOps(PointInTimeSpec{Sessions: 2, EventsPerSession: 5, ReadsPerSession: 4, ValueSize: 8, Seed: seed})
		if err != nil {
			t.Fatalf("pointInTimeOps(seed %d): %v", seed, err)
		}
		appends := 0
		reads := 0
		for s, ops := range plan {
			if len(ops) == 0 || ops[0].read {
				t.Fatalf("seed %d: session %d first op is a read, want an append", seed, s)
			}
			tail := 0
			for _, op := range ops {
				if op.read {
					if op.seq == 0 || op.seq > uint64(tail) {
						t.Fatalf("seed %d: session %d plans a read at seq %d with tail %d", seed, s, op.seq, tail)
					}
					reads++
				} else {
					tail++
					appends++
				}
			}
			if tail != 5 {
				t.Fatalf("seed %d: session %d planned %d appends, want 5", seed, s, tail)
			}
		}
		if appends != 10 || reads != 8 {
			t.Fatalf("seed %d: plan holds %d appends and %d reads, want 10 and 8", seed, appends, reads)
		}
	}
}

// TestRunPointInTimeRejectsInvalidSpec pins the spec contract.
func TestRunPointInTimeRejectsInvalidSpec(t *testing.T) {
	base := PointInTimeSpec{Sessions: 1, EventsPerSession: 1, ReadsPerSession: 0, ValueSize: 1, Seed: 1}
	cases := []struct {
		name   string
		mutate func(*PointInTimeSpec)
	}{
		{"zero sessions", func(p *PointInTimeSpec) { p.Sessions = 0 }},
		{"zero events", func(p *PointInTimeSpec) { p.EventsPerSession = 0 }},
		{"negative reads", func(p *PointInTimeSpec) { p.ReadsPerSession = -1 }},
		{"zero value size", func(p *PointInTimeSpec) { p.ValueSize = 0 }},
	}
	for _, tc := range cases {
		spec := base
		tc.mutate(&spec)
		if _, err := RunPointInTime(t.TempDir(), spec); err == nil {
			t.Errorf("%s: RunPointInTime succeeded, want failure", tc.name)
		}
	}
}

// TestRunTTLMix drives the append + TTL-expiry scenario load at smoke
// size and pins the phase shape: per generation one append phase and
// one recreate phase, then one final protected-read phase. The expiry,
// sweep, and merge chain is verified inside the run — a completed run
// is the evidence it happened.
func TestRunTTLMix(t *testing.T) {
	dir := t.TempDir()
	res, err := RunTTLMix(dir, TTLMixSpec{
		Generations: 2, SessionsPerGen: 2, EventsPerSession: 2,
		ValueSize: 32, TTL: 60 * time.Millisecond, SweepPeriod: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunTTLMix: %v", err)
	}
	if res.Driver != "elena" || res.Workload != "elena-ttl-mix" {
		t.Fatalf("driver/workload = %q/%q, want elena/elena-ttl-mix", res.Driver, res.Workload)
	}
	if len(res.Phases) != 5 {
		t.Fatalf("phases = %d, want 5 (append+recreate per generation, plus protected read)", len(res.Phases))
	}
	wantNames := []string{"ttl-append-g0", "ttl-recreate-g0", "ttl-append-g1", "ttl-recreate-g1", "ttl-protected-read"}
	for i, want := range wantNames {
		if res.Phases[i].Phase != want {
			t.Errorf("phase %d = %q, want %q", i, res.Phases[i].Phase, want)
		}
		if res.Phases[i].Ops <= 0 || res.Phases[i].Throughput <= 0 {
			t.Errorf("phase %q holds ops %d throughput %g, want both positive", want, res.Phases[i].Ops, res.Phases[i].Throughput)
		}
	}
	// Append phase ops: 2 creates + 2 sessions x 2 events + 2 protected events.
	if res.Phases[0].Ops != 8 {
		t.Errorf("ttl-append-g0 ops = %d, want 8", res.Phases[0].Ops)
	}
	// Recreate phase ops: 2 creates + 2 first events of the fresh trajectories.
	if res.Phases[1].Ops != 4 {
		t.Errorf("ttl-recreate-g0 ops = %d, want 4", res.Phases[1].Ops)
	}
	// Protected read: every event of the surviving trajectory.
	if res.Phases[4].Ops != 4 {
		t.Errorf("ttl-protected-read ops = %d, want 4", res.Phases[4].Ops)
	}

	// The protected trajectory survived every merge: reopen shows all
	// its events on disk.
	st, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st.Close() }()
	sess, err := st.CreateSession(elenaNamespace, ttlProtectedName)
	if err != nil {
		t.Fatalf("CreateSession protected: %v", err)
	}
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events protected: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("protected trajectory holds %d events after reopen, want 4", len(events))
	}
}

// TestRunTTLMixRejectsInvalidSpec pins the spec contract: every count
// positive, both durations positive.
func TestRunTTLMixRejectsInvalidSpec(t *testing.T) {
	base := TTLMixSpec{Generations: 1, SessionsPerGen: 1, EventsPerSession: 1,
		ValueSize: 16, TTL: time.Second, SweepPeriod: 50 * time.Millisecond}
	cases := []struct {
		name   string
		mutate func(*TTLMixSpec)
	}{
		{"zero generations", func(s *TTLMixSpec) { s.Generations = 0 }},
		{"zero sessions", func(s *TTLMixSpec) { s.SessionsPerGen = 0 }},
		{"zero events", func(s *TTLMixSpec) { s.EventsPerSession = 0 }},
		{"zero value size", func(s *TTLMixSpec) { s.ValueSize = 0 }},
		{"zero ttl", func(s *TTLMixSpec) { s.TTL = 0 }},
		{"zero sweep period", func(s *TTLMixSpec) { s.SweepPeriod = 0 }},
	}
	for _, tc := range cases {
		spec := base
		tc.mutate(&spec)
		if _, err := RunTTLMix(t.TempDir(), spec); err == nil {
			t.Errorf("%s: RunTTLMix succeeded, want failure", tc.name)
		}
	}
}

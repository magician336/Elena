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

// TestTTLLiveEntriesAfterGeneration pins the reclamation identity with
// worked examples: one metadata entry plus one event per generation,
// independent of how many timed sessions each generation held.
func TestTTLLiveEntriesAfterGeneration(t *testing.T) {
	spec := TTLMixSpec{Generations: 4, SessionsPerGen: 32, EventsPerSession: 5, ValueSize: 16,
		TTL: time.Second, SweepPeriod: 50 * time.Millisecond}
	cases := []struct {
		g    int
		want uint64
	}{
		{0, 6},
		{1, 11},
		{3, 21},
	}
	for _, tc := range cases {
		if got := ttlLiveEntriesAfterGeneration(spec, tc.g); got != tc.want {
			t.Errorf("generation %d identity = %d, want %d", tc.g, got, tc.want)
		}
	}
}

// TestAwaitGenerationReclaimedBindsToGeneration pins the generation
// binding of the TTL scenario's reclamation wait. Consecutive
// generations hold two deadline populations whose deadlines sit close:
// the previous generation's recreated sessions expire shortly before
// the current generation's timed sessions, so a sweep tick between the
// two deadlines merges the earlier population. That merge must not
// satisfy the current generation's wait — the exact orchestration race
// the formal run of ticket 09 observed. The interleaving is built by
// hand against a real store, sweep off, with the test itself playing
// the sweep's merge ticks, so the racing window is constructed from
// the two TTLs instead of sub-second write latencies and the race
// reproduces deterministically on any disk.
func TestAwaitGenerationReclaimedBindsToGeneration(t *testing.T) {
	const (
		sessions   = 3
		events     = 2
		ttlPrior   = 300 * time.Millisecond
		ttlCurrent = 900 * time.Millisecond
	)
	store, err := elena.Open(t.TempDir(), elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	// The perpetual protected trajectory: one metadata entry plus four
	// events, the live-entry floor once every timed population is gone.
	protected, err := store.CreateSession(elenaNamespace, ttlProtectedName)
	if err != nil {
		t.Fatalf("create protected: %v", err)
	}
	for i := 0; i < sessions+1; i++ {
		if _, err := protected.Append([]byte("protected")); err != nil {
			t.Fatalf("append protected %d: %v", i, err)
		}
	}
	if got := store.Stats().LiveEntries; got != 5 {
		t.Fatalf("live entries after protected = %d, want 5 (metadata plus 4 events)", got)
	}

	// Generation 0's timed population: sessions × (metadata + events)
	// entries, all carrying the earlier deadline.
	prior := make([]*elena.Session, sessions)
	for s := range prior {
		prior[s], err = store.CreateSessionWithTTL(elenaNamespace, ttlTimedName(0, s), ttlPrior)
		if err != nil {
			t.Fatalf("create prior %d: %v", s, err)
		}
		for e := 0; e < events; e++ {
			if _, err := prior[s].Append([]byte("prior")); err != nil {
				t.Fatalf("append prior %d: %v", s, err)
			}
		}
	}
	if got := store.Stats().LiveEntries; got != 5+sessions*(events+1) {
		t.Fatalf("live entries after prior generation = %d, want %d", got, 5+sessions*(events+1))
	}

	// Generation 0's reclamation: the deadline elapses, one merge
	// reclaims its bytes, and the wait for merge 1 returns — the
	// single-generation chain, still correct.
	time.Sleep(ttlPrior + 100*time.Millisecond)
	if err := store.Merge(); err != nil {
		t.Fatalf("merge prior generation: %v", err)
	}
	if got := store.Stats().LiveEntries; got != 5 {
		t.Fatalf("live entries after prior merge = %d, want 5", got)
	}

	// The previous generation's recreate population (one metadata and
	// one event per session, ttlPrior again) and the current
	// generation's timed population (ttlCurrent, created right after):
	// their deadlines differ by the TTL gap — around 600ms regardless
	// of disk speed — with the recreate deadline first, exactly the
	// formal run's ordering.
	for s := range prior {
		sess, err := store.CreateSessionWithTTL(elenaNamespace, ttlTimedName(0, s), ttlPrior)
		if err != nil {
			t.Fatalf("recreate prior %d: %v", s, err)
		}
		if _, err := sess.Append([]byte("recreated")); err != nil {
			t.Fatalf("append recreated %d: %v", s, err)
		}
	}
	current := make([]*elena.Session, sessions)
	for s := range current {
		current[s], err = store.CreateSessionWithTTL(elenaNamespace, ttlTimedName(1, s), ttlCurrent)
		if err != nil {
			t.Fatalf("create current %d: %v", s, err)
		}
		for e := 0; e < events; e++ {
			if _, err := current[s].Append([]byte("current")); err != nil {
				t.Fatalf("append current %d: %v", s, err)
			}
		}
	}
	mergesBefore := store.Stats().OpsMerge
	if got := store.Stats().LiveEntries; got != 5+2*sessions+sessions*(events+1) {
		t.Fatalf("live entries with both populations = %d, want %d", got, 5+2*sessions+sessions*(events+1))
	}

	// The wait for generation 1 starts, then the racing merge lands:
	// the recreate population's deadline has elapsed, the current
	// generation's has not, and the sweep's merge reclaims the earlier
	// bytes. The wait must keep waiting — nothing may report
	// generation 1 reclaimed while its sessions still hold events.
	awaited := make(chan error, 1)
	want := uint64(1 + 2*events) // protected metadata plus its events
	go func() {
		awaited <- awaitGenerationReclaimed(store, want, mergesBefore, 2*time.Second)
	}()
	time.Sleep(60 * time.Millisecond)
	time.Sleep(ttlPrior) // the recreate deadline elapses; the current one is still ~600ms away
	if err := store.Merge(); err != nil {
		t.Fatalf("racing merge: %v", err)
	}
	select {
	case err := <-awaited:
		t.Fatalf("reclamation wait returned %v while the current generation still holds events — generation binding broken", err)
	case <-time.After(300 * time.Millisecond):
	}

	// The current generation's own deadline elapses and its merge
	// runs: the wait returns, and the live-entry count has fallen to
	// the protected floor.
	time.Sleep(ttlCurrent)
	if err := store.Merge(); err != nil {
		t.Fatalf("current merge: %v", err)
	}
	select {
	case err := <-awaited:
		if err != nil {
			t.Fatalf("reclamation wait after current merge: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reclamation wait did not return after the current generation's merge")
	}
	if got := store.Stats().LiveEntries; got != 5 {
		t.Fatalf("live entries after current merge = %d, want 5", got)
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

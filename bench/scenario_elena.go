package bench

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/magician336/elena"
)

// scenarioSessionName renders one scenario session's name: the scenario
// prefix keeps the populations of one directory distinguishable — the
// TTL scenario's timed sessions against its perpetual one, for
// instance.
func scenarioSessionName(scenario string, i int) string {
	return fmt.Sprintf("%s-%d", scenario, i)
}

// AppendOnlySpec declares the pure-append Elena scenario load:
// Sessions trajectories in the bench namespace, each receiving
// EventsPerSession events of ValueSize bytes, driven in round-robin
// order so the interleaving of many growing trajectories is the load.
// Every field must be positive.
type AppendOnlySpec struct {
	Sessions         int
	EventsPerSession int
	ValueSize        int
}

// RunAppendOnly drives the pure-append scenario load through the public
// elena library: it opens a fresh store under dir with the background
// sweep off, creates the sessions ahead of the timed window, then times
// one append-only phase over every event — session creation is setup,
// the append stream is the load. Each append verifies the sequence
// number the store acknowledged continues its trajectory hole-free, so
// a lost or reordered event fails the run instead of passing silently.
func RunAppendOnly(dir string, spec AppendOnlySpec) (Result, error) {
	if spec.Sessions <= 0 {
		return Result{}, fmt.Errorf("append-only scenario: sessions %d must be positive", spec.Sessions)
	}
	if spec.EventsPerSession <= 0 {
		return Result{}, fmt.Errorf("append-only scenario: events per session %d must be positive", spec.EventsPerSession)
	}
	if spec.ValueSize <= 0 {
		return Result{}, fmt.Errorf("append-only scenario: value size %d must be positive", spec.ValueSize)
	}
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return Result{}, fmt.Errorf("append-only scenario: open: %w", err)
	}
	sessions := make([]*elena.Session, spec.Sessions)
	for s := range sessions {
		sess, err := store.CreateSession(elenaNamespace, scenarioSessionName("append", s))
		if err != nil {
			return Result{}, joinClose(store, fmt.Errorf("append-only scenario: create session %d: %w", s, err))
		}
		sessions[s] = sess
	}

	var lat Latency
	start := time.Now()
	for e := 0; e < spec.EventsPerSession; e++ {
		for s, sess := range sessions {
			opStart := time.Now()
			seq, err := sess.Append(genValue(uint64(s), e, spec.ValueSize))
			if err != nil {
				return Result{}, joinClose(store, fmt.Errorf("append-only scenario: append session %d event %d: %w", s, e, err))
			}
			lat.Observe(time.Since(opStart))
			if seq != uint64(e)+1 {
				return Result{}, joinClose(store, fmt.Errorf("append-only scenario: append session %d event %d acknowledged seq %d, want %d",
					s, e, seq, e+1))
			}
		}
	}
	elapsed := time.Since(start)
	if err := store.Close(); err != nil {
		return Result{}, fmt.Errorf("append-only scenario: close: %w", err)
	}
	return Result{
		Driver:   "elena",
		Workload: "elena-append-only",
		Phases: []PhaseResult{{
			Phase:      "append-only",
			Ops:        spec.Sessions * spec.EventsPerSession,
			Elapsed:    elapsed,
			Throughput: Throughput(spec.Sessions*spec.EventsPerSession, elapsed),
			Latency:    lat.Summarize(),
		}},
	}, nil
}

// joinClose pairs a mid-run failure with the store's Close error the
// way the harness does: the run error leads, the close error rides
// along instead of being dropped on the floor.
func joinClose(store *elena.Store, runErr error) error {
	return fmt.Errorf("%w (close: %v)", runErr, store.Close())
}

// PointInTimeSpec declares the append + point-in-time-read scenario
// load: Sessions trajectories, each planned to receive
// EventsPerSession appends and ReadsPerSession point-in-time reads
// interleaved by Seed, with ValueSize-byte payloads. The plan is pure:
// one seed, one operation stream, every time. Sessions and events must
// be positive, reads non-negative, the value size positive.
type PointInTimeSpec struct {
	Sessions         int
	EventsPerSession int
	ReadsPerSession  int
	ValueSize        int
	Seed             uint64
}

// pointInTimeStep is one planned operation of the point-in-time mix:
// either the trajectory's next append or a point-in-time read up to
// seq.
type pointInTimeStep struct {
	read bool
	seq  uint64 // point-in-time read bound; zero for appends
}

// pointInTimeOps plans the spec's seeded operation stream: per session,
// one leading append — a read before any append has nothing to read —
// then the remaining appends and the reads shuffled together. Each
// read's seq is drawn uniformly from the appends its session has
// already planned, so every read resolves to a non-empty trajectory
// prefix the run can verify in full.
func pointInTimeOps(spec PointInTimeSpec) ([][]pointInTimeStep, error) {
	r := rand.New(rand.NewPCG(spec.Seed, genStream))
	plan := make([][]pointInTimeStep, spec.Sessions)
	for s := range plan {
		steps := make([]pointInTimeStep, 1, spec.EventsPerSession+spec.ReadsPerSession)
		steps[0] = pointInTimeStep{}
		for i := 1; i < spec.EventsPerSession; i++ {
			steps = append(steps, pointInTimeStep{})
		}
		for i := 0; i < spec.ReadsPerSession; i++ {
			steps = append(steps, pointInTimeStep{read: true})
		}
		r.Shuffle(len(steps)-1, func(i, j int) { steps[i+1], steps[j+1] = steps[j+1], steps[i+1] })
		tail := 0
		for i, step := range steps {
			if step.read {
				steps[i].seq = 1 + r.Uint64N(uint64(tail))
			} else {
				tail++
			}
		}
		plan[s] = steps
	}
	return plan, nil
}

// RunPointInTime drives the append + point-in-time-read scenario load
// through the public elena library: one mixed phase, the planned
// operations of every session interleaved round-robin, each append
// verified hole-free against its trajectory's acknowledged sequence and
// each read verified in full — the events up to seq must be exactly
// seq, in sequence order, with the payloads their appends wrote. A
// point-in-time read that misses events, invents them, or returns
// another write's bytes fails the run instead of passing silently.
func RunPointInTime(dir string, spec PointInTimeSpec) (Result, error) {
	if err := validatePointInTimeSpec(spec); err != nil {
		return Result{}, err
	}
	plan, err := pointInTimeOps(spec)
	if err != nil {
		return Result{}, err
	}
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return Result{}, fmt.Errorf("point-in-time scenario: open: %w", err)
	}
	sessions := make([]*elena.Session, spec.Sessions)
	for s := range sessions {
		sess, err := store.CreateSession(elenaNamespace, scenarioSessionName("point", s))
		if err != nil {
			return Result{}, joinClose(store, fmt.Errorf("point-in-time scenario: create session %d: %w", s, err))
		}
		sessions[s] = sess
	}

	total := spec.Sessions * (spec.EventsPerSession + spec.ReadsPerSession)
	var lat Latency
	start := time.Now()
	done := 0
	for i := 0; done < total; i++ {
		for s, sess := range sessions {
			if i >= len(plan[s]) {
				continue
			}
			step := plan[s][i]
			opStart := time.Now()
			if step.read {
				err := verifyPointInTime(sess, uint64(s), step.seq, spec.ValueSize)
				if err != nil {
					return Result{}, joinClose(store, fmt.Errorf("point-in-time scenario: session %d: %w", s, err))
				}
			} else {
				if err := appendVerified(sess, uint64(s), spec.ValueSize); err != nil {
					return Result{}, joinClose(store, fmt.Errorf("point-in-time scenario: session %d: %w", s, err))
				}
			}
			lat.Observe(time.Since(opStart))
			done++
		}
	}
	elapsed := time.Since(start)
	if err := store.Close(); err != nil {
		return Result{}, fmt.Errorf("point-in-time scenario: close: %w", err)
	}
	return Result{
		Driver:   "elena",
		Workload: "elena-point-in-time",
		Phases: []PhaseResult{{
			Phase:      "point-in-time",
			Ops:        total,
			Elapsed:    elapsed,
			Throughput: Throughput(total, elapsed),
			Latency:    lat.Summarize(),
		}},
	}, nil
}

// validatePointInTimeSpec checks the spec's declared shape.
func validatePointInTimeSpec(spec PointInTimeSpec) error {
	if spec.Sessions <= 0 {
		return fmt.Errorf("point-in-time scenario: sessions %d must be positive", spec.Sessions)
	}
	if spec.EventsPerSession <= 0 {
		return fmt.Errorf("point-in-time scenario: events per session %d must be positive", spec.EventsPerSession)
	}
	if spec.ReadsPerSession < 0 {
		return fmt.Errorf("point-in-time scenario: reads per session %d must not be negative", spec.ReadsPerSession)
	}
	if spec.ValueSize <= 0 {
		return fmt.Errorf("point-in-time scenario: value size %d must be positive", spec.ValueSize)
	}
	return nil
}

// appendVerified appends the value derived from session key and the
// trajectory's next write ordinal, then checks the acknowledged
// sequence continues the trajectory hole-free. The run is sequential,
// so the expected sequence is exact.
func appendVerified(sess *elena.Session, key uint64, valueSize int) error {
	// The session's event count is queried through the public API: the
	// event total is the write ordinal, both start at the trajectory's
	// first sequence.
	events, err := sess.Events()
	if err != nil {
		return err
	}
	ordinal := len(events)
	seq, err := sess.Append(genValue(key, ordinal, valueSize))
	if err != nil {
		return err
	}
	if seq != uint64(ordinal)+1 {
		return fmt.Errorf("append acknowledged seq %d, want %d", seq, ordinal+1)
	}
	return nil
}

// verifyPointInTime reads the trajectory up to seq and checks the
// point in time in full: exactly seq events, in sequence order, each
// carrying the payload its append wrote.
func verifyPointInTime(sess *elena.Session, key uint64, seq uint64, valueSize int) error {
	events, err := sess.EventsUpTo(seq)
	if err != nil {
		return err
	}
	if len(events) != int(seq) {
		return fmt.Errorf("point-in-time read up to %d returned %d events, want %d", seq, len(events), seq)
	}
	for i, event := range events {
		if event.Seq != uint64(i)+1 {
			return fmt.Errorf("point-in-time read up to %d: event %d carries seq %d, want %d", seq, i, event.Seq, i+1)
		}
		want := genValue(key, i, valueSize)
		if !bytes.Equal(event.Payload, want) {
			return fmt.Errorf("point-in-time read up to %d: event %d payload differs from what its append wrote", seq, i)
		}
	}
	return nil
}

// ttlProtectedName is the one perpetual session of the TTL scenario:
// its trajectory keeps growing while every timed generation around it
// expires, and the final phase reads it back whole — the load's proof
// that merge reclaims only the elapsed sessions' bytes.
const ttlProtectedName = "ttl-protected"

// ttlProtectedKey is the protected session's value-derivation key,
// disjoint from every timed session's key so no payload collides.
const ttlProtectedKey uint64 = 1 << 62

// TTLMixSpec declares the append + TTL-expiry scenario load:
// Generations rounds, each creating SessionsPerGen timed sessions of
// TTL and appending EventsPerSession events of ValueSize bytes to each
// — plus the same to the one perpetual protected session — then
// waiting for the background sweep, tuned to SweepPeriod, to merge the
// elapsed generation away before the next round recreates the freed
// names. The TTL must outlast one generation's append phase: appending
// to an elapsed session fails by design. Every count and both
// durations must be positive.
type TTLMixSpec struct {
	Generations      int
	SessionsPerGen   int
	EventsPerSession int
	ValueSize        int
	TTL              time.Duration
	SweepPeriod      time.Duration
}

// RunTTLMix drives the append + TTL-expiry scenario load through the
// public elena library, covering the reclamation chain under load:
// timed sessions are created and filled (phase ttl-append-gN), their
// deadlines elapse, the background sweep notices and merges — observed
// through Stats as the merge count rising while the live-entry count
// falls — the elapsed sessions read as never written (phase-exit
// checks on Events, Event, and Append), and the freed names are
// recreated with fresh trajectories starting at sequence 1 (phase
// ttl-recreate-gN). The waiting between phases is orchestration, not
// load: only the library calls inside phases are timed. A final
// ttl-protected-read phase reads the perpetual trajectory back whole,
// event by event, verifying merge reclaimed nothing that stayed live.
func RunTTLMix(dir string, spec TTLMixSpec) (Result, error) {
	if err := validateTTLMixSpec(spec); err != nil {
		return Result{}, err
	}
	store, err := elena.Open(dir, elena.WithSweepPeriod(spec.SweepPeriod))
	if err != nil {
		return Result{}, fmt.Errorf("ttl-mix scenario: open: %w", err)
	}
	fail := func(err error) (Result, error) {
		return Result{}, joinClose(store, err)
	}
	protected, err := store.CreateSession(elenaNamespace, ttlProtectedName)
	if err != nil {
		return fail(fmt.Errorf("ttl-mix scenario: create protected session: %w", err))
	}

	res := Result{Driver: "elena", Workload: "elena-ttl-mix"}
	protectedOrdinal := 0
	for g := 0; g < spec.Generations; g++ {
		mergesAtGenStart := store.Stats().OpsMerge
		appendPhase := newTimedPhase(fmt.Sprintf("ttl-append-g%d", g))
		sessions := make([]*elena.Session, spec.SessionsPerGen)
		for s := range sessions {
			s := s
			if err := appendPhase.step(func() error {
				sess, err := store.CreateSessionWithTTL(elenaNamespace, ttlTimedName(g, s), spec.TTL)
				sessions[s] = sess
				return err
			}); err != nil {
				return fail(fmt.Errorf("ttl-mix scenario: create g%d s%d: %w", g, s, err))
			}
		}
		for e := 0; e < spec.EventsPerSession; e++ {
			for s := range sessions {
				s := s
				if err := appendPhase.step(func() error {
					return appendVerifiedSeq(sessions[s], ttlTimedKey(g, s), e, spec.ValueSize)
				}); err != nil {
					return fail(fmt.Errorf("ttl-mix scenario: append g%d s%d: %w", g, s, err))
				}
			}
			ordinal := protectedOrdinal + e
			if err := appendPhase.step(func() error {
				return appendVerifiedSeq(protected, ttlProtectedKey, ordinal, spec.ValueSize)
			}); err != nil {
				return fail(fmt.Errorf("ttl-mix scenario: append protected: %w", err))
			}
		}
		res.Phases = append(res.Phases, appendPhase.result())
		protectedOrdinal += spec.EventsPerSession

		if err := awaitGenerationReclaimed(store,
			ttlLiveEntriesAfterGeneration(spec, g), mergesAtGenStart,
			spec.TTL+ttlReclaimSlack); err != nil {
			return fail(fmt.Errorf("ttl-mix scenario: generation %d: %w", g, err))
		}
		if err := verifyElapsed(store, sessions, g); err != nil {
			return fail(fmt.Errorf("ttl-mix scenario: generation %d elapsed checks: %w", g, err))
		}

		recreatePhase := newTimedPhase(fmt.Sprintf("ttl-recreate-g%d", g))
		for s := range sessions {
			s := s
			var fresh *elena.Session
			if err := recreatePhase.step(func() error {
				var err error
				fresh, err = store.CreateSessionWithTTL(elenaNamespace, ttlTimedName(g, s), spec.TTL)
				return err
			}); err != nil {
				return fail(fmt.Errorf("ttl-mix scenario: recreate g%d s%d: %w", g, s, err))
			}
			if err := recreatePhase.step(func() error {
				return appendVerifiedSeq(fresh, ttlTimedKey(g, s), 0, spec.ValueSize)
			}); err != nil {
				return fail(fmt.Errorf("ttl-mix scenario: recreate g%d s%d: %w", g, s, err))
			}
		}
		res.Phases = append(res.Phases, recreatePhase.result())
	}

	readPhase := newTimedPhase("ttl-protected-read")
	for i := 0; i < protectedOrdinal; i++ {
		if err := readPhase.step(func() error {
			event, err := protected.Event(uint64(i) + 1)
			if err != nil {
				return err
			}
			if want := genValue(ttlProtectedKey, i, spec.ValueSize); !bytes.Equal(event.Payload, want) {
				return fmt.Errorf("protected event %d payload differs from what its append wrote", i+1)
			}
			return nil
		}); err != nil {
			return fail(fmt.Errorf("ttl-mix scenario: protected read: %w", err))
		}
	}
	res.Phases = append(res.Phases, readPhase.result())

	if err := store.Close(); err != nil {
		return Result{}, fmt.Errorf("ttl-mix scenario: close: %w", err)
	}
	return res, nil
}

// validateTTLMixSpec checks the spec's declared shape.
func validateTTLMixSpec(spec TTLMixSpec) error {
	switch {
	case spec.Generations <= 0:
		return fmt.Errorf("ttl-mix scenario: generations %d must be positive", spec.Generations)
	case spec.SessionsPerGen <= 0:
		return fmt.Errorf("ttl-mix scenario: sessions per generation %d must be positive", spec.SessionsPerGen)
	case spec.EventsPerSession <= 0:
		return fmt.Errorf("ttl-mix scenario: events per session %d must be positive", spec.EventsPerSession)
	case spec.ValueSize <= 0:
		return fmt.Errorf("ttl-mix scenario: value size %d must be positive", spec.ValueSize)
	case spec.TTL <= 0:
		return fmt.Errorf("ttl-mix scenario: ttl %s must be positive", spec.TTL)
	case spec.SweepPeriod <= 0:
		return fmt.Errorf("ttl-mix scenario: sweep period %s must be positive", spec.SweepPeriod)
	}
	return nil
}

// ttlTimedName renders generation g's session s's name; recreating the
// name after its deadline is the name-free check.
func ttlTimedName(g, s int) string {
	return scenarioSessionName(fmt.Sprintf("ttl-g%d", g), s)
}

// ttlTimedKey derives session (g, s)'s value-derivation key, disjoint
// across generations and sessions.
func ttlTimedKey(g, s int) uint64 {
	return uint64(g)<<32 | uint64(s)
}

// timedPhase times one scenario phase: each step is one library call
// observed individually, and the phase's wall clock spans its first
// step's start to its last step's return.
type timedPhase struct {
	name  string
	lat   Latency
	ops   int
	start time.Time
	end   time.Time
}

// newTimedPhase prepares an empty phase under one report name.
func newTimedPhase(name string) *timedPhase {
	return &timedPhase{name: name}
}

// step runs fn as one timed operation of the phase.
func (p *timedPhase) step(fn func() error) error {
	stepStart := time.Now()
	if p.ops == 0 {
		p.start = stepStart
	}
	err := fn()
	p.end = time.Now()
	p.lat.Observe(p.end.Sub(stepStart))
	p.ops++
	return err
}

// result freezes the phase into its PhaseResult.
func (p *timedPhase) result() PhaseResult {
	elapsed := p.end.Sub(p.start)
	return PhaseResult{
		Phase:      p.name,
		Ops:        p.ops,
		Elapsed:    elapsed,
		Throughput: Throughput(p.ops, elapsed),
		Latency:    p.lat.Summarize(),
	}
}

// ttlReclaimSlack is the grace the reclamation wait grants beyond the
// TTL itself: the wait begins after the append phase, so the deadline
// sits at most one TTL away and the sweep adds at most one period plus
// the merge's own duration. Five seconds absorbs all three on any
// disk the scenario runs on.
const ttlReclaimSlack = 5 * time.Second

// ttlLiveEntriesAfterGeneration returns the store's exact live-entry
// count once generation g's timed population is reclaimed: the
// protected trajectory's metadata entry plus its events through
// generation g. Every other entry the scenario wrote by then carries a
// deadline that has already elapsed — the previous generations'
// recreated sessions expire before the current generation's timed
// sessions — and one merge drops every expired entry on disk, whichever
// population triggered it. The count is therefore an identity, not an
// estimate: while generation g's sessions hold unreclaimed bytes the
// count sits SessionsPerGen*(EventsPerSession+1) entries above it, and
// no other population's merge can close that gap.
func ttlLiveEntriesAfterGeneration(spec TTLMixSpec, g int) uint64 {
	return uint64(1 + (g+1)*spec.EventsPerSession)
}

// awaitGenerationReclaimed blocks until the current generation's timed
// sessions are physically reclaimed — the live-entry count has fallen
// to the generation's known identity want and at least one merge has
// run since the generation began (mergesBefore) — or fails after
// timeout. The condition is generation-bound where the merge count it
// replaced was global: consecutive generations hold deadline
// populations whose deadlines sit sub-second apart, and the merge that
// reclaims the previous generation's recreated sessions raises the
// global merge count and lowers the live-entry total without
// touching the current generation's bytes — under the count-based wait
// that premature satisfaction let the elapsed checks run against
// sessions that had not expired yet. The identity check cannot be
// satisfied early: expired-but-unmerged entries still count as live,
// so the count reaches want only when a merge has dropped every timed
// entry up to and including generation g's.
func awaitGenerationReclaimed(store *elena.Store, want, mergesBefore uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		stats := store.Stats()
		if stats.LiveEntries == want && stats.OpsMerge > mergesBefore {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for generation reclamation: live entries %d want %d, merges %d started at %d; Stats %+v",
				stats.LiveEntries, want, stats.OpsMerge, mergesBefore, stats)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// verifyElapsed checks the elapsed generation through the public read
// and write paths: the whole trajectory reads as never written, the
// single-event read finds nothing, and an append is refused — a write
// now would resurrect half of one.
func verifyElapsed(store *elena.Store, sessions []*elena.Session, g int) error {
	for s, sess := range sessions {
		events, err := sess.Events()
		if err != nil {
			return fmt.Errorf("events of elapsed g%d s%d: %w", g, s, err)
		}
		if len(events) != 0 {
			return fmt.Errorf("elapsed g%d s%d reads %d events, want 0", g, s, len(events))
		}
		if _, err := sess.Event(1); !errors.Is(err, elena.ErrEventNotFound) {
			return fmt.Errorf("elapsed g%d s%d single-event read: error %v, want ErrEventNotFound", g, s, err)
		}
		if _, err := sess.Append([]byte("late")); !errors.Is(err, elena.ErrSessionEnded) {
			return fmt.Errorf("elapsed g%d s%d append: error %v, want ErrSessionEnded", g, s, err)
		}
	}
	return nil
}

// appendVerifiedSeq appends the value derived from (key, ordinal) and
// checks the acknowledged sequence is ordinal+1 — the trajectory's
// expected next event.
func appendVerifiedSeq(sess *elena.Session, key uint64, ordinal int, valueSize int) error {
	seq, err := sess.Append(genValue(key, ordinal, valueSize))
	if err != nil {
		return err
	}
	if seq != uint64(ordinal)+1 {
		return fmt.Errorf("append acknowledged seq %d, want %d", seq, ordinal+1)
	}
	return nil
}

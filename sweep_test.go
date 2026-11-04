package elena_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"elena"
)

// waitForStats polls Stats until cond passes, failing the test when the
// condition never holds within a generous deadline. The background sweep
// acts asynchronously — a deadline elapses, the next period's check
// notices, the merge runs — so reclamation is observed by polling the
// same port the contrast assertions of the TTL tests use: facade Stats.
func waitForStats(t *testing.T, st *elena.Store, what string, cond func(elena.Stats) bool) elena.Stats {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stats := st.Stats()
		if cond(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last Stats: %+v", what, stats)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A merge must physically reclaim the bytes of an elapsed session: the
// TTL tests held that DataBytes, LiveBytes, and the entry counts stand
// exactly where they stood when the deadline passed — this test is that
// contrast assertion's other half. One Merge later, the whole session —
// metadata and events — is gone from the byte and index figures, and
// the session was already invisible before, so visibility does not move.
func TestMergeReclaimsElapsedSessionBytes(t *testing.T) {
	st := openStore(t)
	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}

	before := st.Stats()
	if before.DataBytes == 0 {
		t.Fatal("Stats().DataBytes = 0, want the stored session and its events to occupy bytes")
	}
	if before.LiveEntries < 4 { // the metadata entry plus three events
		t.Fatalf("Stats().LiveEntries = %d, want at least 4", before.LiveEntries)
	}

	waitForTTLElapse(ttl)

	if err := st.Merge(); err != nil {
		t.Fatalf("Merge() after the deadline failed: %v", err)
	}

	after := st.Stats()
	for _, tc := range []struct {
		name string
		got  uint64
	}{
		{"LiveEntries", after.LiveEntries},
		{"DataBytes", after.DataBytes},
		{"LiveBytes", after.LiveBytes},
		{"DeadBytes", after.DeadBytes},
	} {
		if tc.got != 0 {
			t.Errorf("Stats().%s after Merge() = %d, want 0 — the elapsed session's bytes must be reclaimed", tc.name, tc.got)
		}
	}
	if after.OpsMerge != before.OpsMerge+1 {
		t.Errorf("Stats().OpsMerge after Merge() = %d, want %d (one merge ran)", after.OpsMerge, before.OpsMerge+1)
	}

	// The merge reclaims bytes; it must not change visibility — the
	// session read as gone before the merge and still does.
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after Merge() failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after Merge() returned %d events, want 0", len(events))
	}
}

// With a configured sweep period, an elapsed session is reclaimed with
// no manual call at all: the sweep notices the deadline on its next
// check and the merge it triggers returns the bytes — the contrast
// assertion of the TTL tests reversed through the background path this
// time. Further checks after the reclaim are no-ops — the sweep is
// idempotent, exactly one merge runs — and visibility was gone the
// moment the deadline passed, not when the sweep acted.
func TestSweepReclaimsElapsedSessionAutomatically(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepPeriod(25*time.Millisecond))
	if err != nil {
		t.Fatalf("Open(WithSweepPeriod) failed: %v", err)
	}
	defer closeStore(t, st)

	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}

	waitForStats(t, st, "the sweep to reclaim the elapsed session", func(s elena.Stats) bool {
		return s.LiveEntries == 0 && s.DataBytes == 0
	})
	merged := waitForStats(t, st, "the sweep's merge to be counted", func(s elena.Stats) bool {
		return s.OpsMerge >= 1
	})
	if merged.OpsMerge != 1 {
		t.Errorf("Stats().OpsMerge = %d, want exactly 1 — one signal, one merge", merged.OpsMerge)
	}

	// Idempotence: several more periods change nothing. The signal was
	// consumed, the bytes are back, and repeated sweeps have no side
	// effect to have.
	time.Sleep(150 * time.Millisecond)
	settled := st.Stats()
	for _, tc := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"LiveEntries", settled.LiveEntries, 0},
		{"DataBytes", settled.DataBytes, 0},
		{"LiveBytes", settled.LiveBytes, 0},
		{"DeadBytes", settled.DeadBytes, 0},
		{"OpsMerge", settled.OpsMerge, merged.OpsMerge},
	} {
		if tc.got != tc.want {
			t.Errorf("Stats().%s after further sweeps = %d, want %d (unchanged)", tc.name, tc.got, tc.want)
		}
	}

	// Visibility was gone at the deadline, before and regardless of the
	// sweep acting.
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the sweep failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the sweep returned %d events, want 0", len(events))
	}
	if _, err := sess.Event(1); !errors.Is(err, elena.ErrEventNotFound) {
		t.Errorf("Event(1) after the sweep err = %v, want ErrEventNotFound", err)
	}
	sessions, err := st.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after the sweep failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("Sessions() after the sweep returned %d sessions, want 0", len(sessions))
	}
}

// WithSweepDisabled no background merge may ever run: expiration is
// purely lazy. The elapsed session is invisible at once, but its bytes
// stand on disk — OpsMerge stays 0 — until a manual Merge reclaims
// them.
func TestSweepDisabledLeavesReclamationToManualMerge(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("Open(WithSweepDisabled) failed: %v", err)
	}
	defer closeStore(t, st)

	const ttl = 300 * time.Millisecond
	sess := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, sess, "event")
	}
	before := st.Stats()

	waitForTTLElapse(ttl)
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() after the deadline returned %d events, want 0", len(events))
	}

	// Far beyond a period later, nothing has reclaimed the bytes: with
	// the sweep off, no merge runs on its own.
	time.Sleep(300 * time.Millisecond)
	idle := st.Stats()
	if idle.OpsMerge != 0 {
		t.Errorf("Stats().OpsMerge = %d, want 0 — no merge may run with the sweep disabled", idle.OpsMerge)
	}
	if idle.DataBytes != before.DataBytes || idle.LiveEntries != before.LiveEntries {
		t.Errorf("Stats() with the sweep disabled drifted: DataBytes %d→%d, LiveEntries %d→%d, want unchanged",
			before.DataBytes, idle.DataBytes, before.LiveEntries, idle.LiveEntries)
	}

	if err := st.Merge(); err != nil {
		t.Fatalf("Merge() with the sweep disabled failed: %v", err)
	}
	after := st.Stats()
	if after.DataBytes != 0 || after.LiveEntries != 0 {
		t.Errorf("Stats() after manual Merge = (DataBytes %d, LiveEntries %d), want (0, 0)", after.DataBytes, after.LiveEntries)
	}
}

// The sweep reclaims elapsed timed sessions; a perpetual session of the
// same store must be untouched by it: its events stay readable, every
// remaining byte stays live, and its trajectory keeps accepting appends
// after the sweep has acted.
func TestSweepLeavesPerpetualSessionsIntact(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepPeriod(25*time.Millisecond))
	if err != nil {
		t.Fatalf("Open(WithSweepPeriod) failed: %v", err)
	}
	defer closeStore(t, st)

	keeper := createSession(t, st, "runs", "keeper")
	mustAppend(t, keeper, "event")
	mustAppend(t, keeper, "event")
	const ttl = 300 * time.Millisecond
	temp := createTimedSession(t, st, "runs", "temp", ttl)
	mustAppend(t, temp, "event")
	mustAppend(t, temp, "event")

	// The sweep reclaims temp: only keeper's metadata and two events
	// remain, and every remaining byte is live.
	final := waitForStats(t, st, "the sweep to reclaim the timed session only", func(s elena.Stats) bool {
		return s.LiveEntries == 3 // keeper's metadata entry plus two events
	})
	if final.DataBytes != final.LiveBytes {
		t.Errorf("Stats() after the sweep: DataBytes = %d, LiveBytes = %d, want equal — nothing dead may remain", final.DataBytes, final.LiveBytes)
	}
	if final.DeadBytes != 0 {
		t.Errorf("Stats().DeadBytes after the sweep = %d, want 0", final.DeadBytes)
	}

	events, err := keeper.Events()
	if err != nil {
		t.Fatalf("Events() of the perpetual session after the sweep failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Events() of the perpetual session after the sweep returned %d events, want 2", len(events))
	}
	if seq, err := keeper.Append([]byte("kept")); err != nil || seq != 3 {
		t.Errorf("Append() to the perpetual session after the sweep = (%d, %v), want (3, nil)", seq, err)
	}

	sessions, err := st.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after the sweep failed: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "keeper" || !sessions[0].Deadline.IsZero() {
		t.Fatalf("Sessions() after the sweep = %+v, want only the perpetual session \"keeper\"", sessions)
	}
}

// A timed session stored by an earlier process, still live when this
// process opens the directory, belongs to the sweep's deadline view
// even though no handle ever attaches to it here: the Open-time scan of
// the namespaces' live session metadata collects its deadline, so its
// bytes come back without this process touching the session at all.
func TestSweepTracksStoredTimedSessionsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	sess := createTimedSession(t, st, "runs", "stored", 400*time.Millisecond)
	for i := 0; i < 2; i++ {
		mustAppend(t, sess, "event")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := elena.Open(dir, elena.WithSweepPeriod(25*time.Millisecond))
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	waitForStats(t, reopened, "the sweep to reclaim the stored session", func(s elena.Stats) bool {
		return s.LiveEntries == 0 && s.DataBytes == 0
	})

	sessions, err := reopened.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after the sweep failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("Sessions() after the sweep returned %d sessions, want 0", len(sessions))
	}
}

// A session that expired before this Open is invisible to the fresh
// store — the engine reports only live entries — so the sweep's initial
// deadline view cannot know it: the boundary is visibility immediate,
// physical reclamation punctual only with a sweep signal. The residue
// lingers on disk (the reopen path still counts it) while the sweep
// idles, and
// the next signal's merge — which drops every expired entry on disk —
// reclaims the residue along with its own session.
func TestSweepReclaimsRestartResidueOnNextSignal(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("first Open(%q) failed: %v", dir, err)
	}
	gone := createTimedSession(t, st, "runs", "gone", 300*time.Millisecond)
	for i := 0; i < 2; i++ {
		mustAppend(t, gone, "event")
	}
	keeper := createSession(t, st, "runs", "keeper")
	mustAppend(t, keeper, "event")
	if err := st.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	waitForTTLElapse(300 * time.Millisecond)

	reopened, err := elena.Open(dir, elena.WithSweepPeriod(25*time.Millisecond))
	if err != nil {
		t.Fatalf("second Open(%q) failed: %v", dir, err)
	}
	defer closeStore(t, reopened)

	// The residue is invisible in listings but still occupies the
	// index: three entries of "gone" plus two of "keeper".
	sessions, err := reopened.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions() after reopen failed: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "keeper" {
		t.Fatalf("Sessions() after reopen = %+v, want only \"keeper\"", sessions)
	}
	if got := reopened.Stats().LiveEntries; got != 5 {
		t.Fatalf("Stats().LiveEntries after reopen = %d, want 5 — the expired residue is still indexed", got)
	}

	// No deadline is pending (the residue was invisible at collection
	// time), so the sweep idles: several periods change nothing.
	time.Sleep(150 * time.Millisecond)
	idle := reopened.Stats()
	if idle.OpsMerge != 0 || idle.LiveEntries != 5 {
		t.Fatalf("the sweep reclaimed without a signal: OpsMerge = %d, LiveEntries = %d, want (0, 5)", idle.OpsMerge, idle.LiveEntries)
	}

	// The next signal's merge reclaims the residue with its own session.
	later := createTimedSession(t, reopened, "runs", "later", 300*time.Millisecond)
	mustAppend(t, later, "event")
	final := waitForStats(t, reopened, "the next signal to reclaim the residue", func(s elena.Stats) bool {
		return s.LiveEntries == 2 && s.DeadBytes == 0 // keeper's metadata and event alone
	})
	if final.DataBytes != final.LiveBytes {
		t.Errorf("Stats() after the sweep: DataBytes = %d, LiveBytes = %d, want equal", final.DataBytes, final.LiveBytes)
	}
	resumed := createSession(t, reopened, "runs", "keeper")
	events, err := resumed.Events()
	if err != nil {
		t.Fatalf("Events() of the perpetual session after the residue sweep failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events() of the perpetual session after the residue sweep returned %d events, want 1", len(events))
	}
}

// Close must join the sweep goroutine before releasing the directory:
// the observable proof is the lock. The engine closes only after the
// join, so a reopen that succeeds right after Close — while the sweep
// was ticking and had even acted once — means the goroutine was already
// gone: deterministic exit, not abandonment. Close stays idempotent
// with the sweep running.
func TestCloseJoinsSweepBeforeReleasingDirectory(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepPeriod(10*time.Millisecond))
	if err != nil {
		t.Fatalf("Open(WithSweepPeriod) failed: %v", err)
	}

	temp := createTimedSession(t, st, "runs", "temp", 200*time.Millisecond)
	for i := 0; i < 3; i++ {
		mustAppend(t, temp, "event")
	}
	// Let the sweep act at least once, then Close while it keeps
	// ticking.
	waitForStats(t, st, "the sweep to reclaim the elapsed session", func(s elena.Stats) bool {
		return s.LiveEntries == 0
	})

	if err := st.Close(); err != nil {
		t.Fatalf("Close() with the sweep running failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close() err = %v, want nil (idempotent)", err)
	}

	reopened, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) right after Close failed: %v — the sweep goroutine was not joined before the lock was released", dir, err)
	}
	closeStore(t, reopened)
}

// The sweep must stay clean under the race detector while the store is
// busy: appends to a perpetual trajectory, a concurrent reader checking
// prefix consistency, short-lived timed sessions being created and
// elapsing under the sweep's nose, and the sweep's merges rewriting
// files around all of it. The perpetual trajectory must come out of the
// storm hole-free and in order, and Close must join the still-ticking
// sweep cleanly at the end.
func TestSweepConcurrentWithReadsAndWrites(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir, elena.WithSweepPeriod(10*time.Millisecond))
	if err != nil {
		t.Fatalf("Open(WithSweepPeriod) failed: %v", err)
	}
	defer closeStore(t, st)

	busy := createSession(t, st, "runs", "busy")

	const writers = 4
	const perWriter = 200
	total := writers * perWriter

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			events, err := busy.Events()
			if err != nil {
				t.Errorf("Events() during the sweep error: %v", err)
				return
			}
			for i, ev := range events {
				if ev.Seq != uint64(i+1) {
					t.Errorf("read observed sequence %d at position %d: not a consistent prefix", ev.Seq, i+1)
					return
				}
			}
		}
	}()

	// A drip of short-lived timed sessions keeps deadlines elapsing so
	// the sweep merges while the writers write. Names cycle; creating a
	// live timed session again is idempotent, and an append may hit a
	// just-elapsed session — both fine, the drip only feeds the sweep.
	dripDone := make(chan struct{})
	go func() {
		defer close(dripDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sess, err := st.CreateSessionWithTTL("runs", fmt.Sprintf("drip-%d", i%8), 80*time.Millisecond)
			if err != nil {
				t.Errorf("CreateSessionWithTTL(drip-%d) error: %v", i%8, err)
				return
			}
			if _, err := sess.Append([]byte("event")); err != nil && !errors.Is(err, elena.ErrSessionEnded) {
				t.Errorf("Append() to drip-%d error: %v", i%8, err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := busy.Append([]byte(fmt.Sprintf("w%d-%d", w, i))); err != nil {
					t.Errorf("Append() error: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-readerDone
	<-dripDone

	events, err := busy.Events()
	if err != nil {
		t.Fatalf("final Events() failed: %v", err)
	}
	if len(events) != total {
		t.Fatalf("final Events() returned %d events, want %d", len(events), total)
	}
	seen := make(map[string]bool, total)
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("final read: sequence %d at position %d — the storm lost or duplicated an event", ev.Seq, i+1)
		}
		if seen[string(ev.Payload)] {
			t.Fatalf("payload %q appears twice — the sequence handed out duplicates", ev.Payload)
		}
		seen[string(ev.Payload)] = true
	}
}

// WithSweepPeriod must reject periods that cannot define a check
// interval — zero or negative — with ErrInvalidSweepPeriod, and hand
// back no store, leaving the directory free to open afterwards.
func TestOpenWithSweepPeriodRejectsInvalidPeriod(t *testing.T) {
	dir := t.TempDir()

	for _, period := range []time.Duration{0, -time.Second} {
		st, err := elena.Open(dir, elena.WithSweepPeriod(period))
		if !errors.Is(err, elena.ErrInvalidSweepPeriod) {
			t.Errorf("Open(WithSweepPeriod(%v)) err = %v, want ErrInvalidSweepPeriod", period, err)
		}
		if st != nil {
			closeStore(t, st)
			t.Error("Open must not return a store on failure")
		}
	}

	// The rejected opens must not have locked the directory.
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) after rejected opens failed: %v", dir, err)
	}
	closeStore(t, st)
}

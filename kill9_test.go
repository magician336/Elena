//go:build unix

package elena_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"elena"
)

// The kill -9 scenarios lift the engine's headline claim — "an
// acknowledged write is durable" — to the semantic layer: a child
// writer drives sessions, events, and checkpoints through the public
// elena API, the parent observes acks on stdout, sends SIGKILL at an
// arbitrary moment, and reopens the directory through the same public
// API. Everything asserted here goes through the fault-injection seam
// (the child harness) and the main seam (Open, CreateSession, Append,
// Events, EventsUpTo, Event, Checkpoints, Restore); nothing inspects
// semantic internals.

// A child appends events across several sessions with plain
// checkpoints between them; the parent kills it after the 40th ack and
// reopens the directory. Every acknowledged event must still be there
// with its exact payload, every trajectory must read as a hole-free
// prefix at least as long as its acknowledged part, and every
// acknowledged checkpoint must still list under its ID. The kill lands
// at an arbitrary point of the child's loop, so unacknowledged events
// may or may not have landed — but the acknowledged prefix can never
// shrink, and a torn event value would fail the payload check.
func TestKill9AllAckedEventsSurvive(t *testing.T) {
	dir := t.TempDir()
	spec := SemanticWriterSpec{
		Dir:             dir,
		Sessions:        3,
		Events:          90,
		PayloadSize:     512,
		Seed:            11,
		CheckpointEvery: 15,
	}

	acked := killSemanticWriterAfterAcks(t, spec, 40)
	if len(acked) < 40 {
		t.Fatalf("observed %d acks before the kill point, want at least 40", len(acked))
	}

	st := openStoreAt(t, dir)
	sessions := openWriterSessions(t, st, spec.Sessions)
	payloads := semanticWriterPayloads(spec)

	// Every acknowledged event is durably there, payload byte-exact.
	for _, ack := range acked {
		if ack.kind != 'A' {
			continue
		}
		ev, err := sessions[ack.session].Event(ack.seq)
		if err != nil {
			t.Fatalf("acked event s%d#%d lost after kill -9 and reopen: %v", ack.session, ack.seq, err)
		}
		want := writerPayloadAt(payloads, spec.Sessions, ack.session, ack.seq)
		if !bytes.Equal(ev.Payload, want) {
			t.Errorf("acked event s%d#%d after kill -9 = %d payload bytes, want the exact acked %d bytes",
				ack.session, ack.seq, len(ev.Payload), len(want))
		}
	}

	// Each trajectory reads as a hole-free prefix — events landed by
	// an append-only, serial writer, truncated only at the front of
	// nothing — at least as long as its acknowledged part, and every
	// visible event carries its exact dataset payload.
	ackedMax := ackedMaxSequence(acked)
	visible := make([]int, spec.Sessions)
	for i, sess := range sessions {
		events, err := sess.Events()
		if err != nil {
			t.Fatalf("Events() of s%d after kill -9 failed: %v", i, err)
		}
		for j, ev := range events {
			if ev.Seq != uint64(j+1) {
				t.Fatalf("Events() of s%d holds sequence %d at position %d — a trajectory must read as a hole-free prefix", i, ev.Seq, j+1)
			}
			want := writerPayloadAt(payloads, spec.Sessions, i, ev.Seq)
			if !bytes.Equal(ev.Payload, want) {
				t.Errorf("visible event s%d#%d after kill -9 = %d payload bytes, want the exact dataset %d bytes",
					i, ev.Seq, len(ev.Payload), len(want))
			}
		}
		visible[i] = len(events)
		if uint64(visible[i]) < ackedMax[i] {
			t.Errorf("s%d shows %d events after kill -9, but %d were acknowledged — the acked prefix shrank", i, visible[i], ackedMax[i])
		}
	}

	// Every acknowledged checkpoint lists under its own ID, and the
	// listed checkpoints form a gap-free run from 1: a plain
	// checkpoint is one engine batch, so recovery keeps a prefix of
	// them, never a half manifest in the middle.
	cps, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after kill -9 failed: %v — a torn manifest would surface here", err)
	}
	assertCheckpointPrefixRun(t, cps, acked)

	// The recovered store stays writable, and the post-kill write
	// survives another reopen alongside the acknowledged events.
	if seq, err := sessions[0].Append([]byte("post-kill")); err != nil || seq != uint64(visible[0]+1) {
		t.Fatalf("Append after kill -9 and reopen = (%d, %v), want (%d, nil)", seq, err, visible[0]+1)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close after kill -9 failed: %v", err)
	}
	final := openStoreAt(t, dir)
	defer closeStore(t, final)
	s0 := createSession(t, final, semanticWriterNamespace, writerSessionName(0))
	ev, err := s0.Event(uint64(visible[0] + 1))
	if err != nil || string(ev.Payload) != "post-kill" {
		t.Errorf("Event(%d) after second reopen = %q, %v; want \"post-kill\", nil", visible[0]+1, ev.Payload, err)
	}
	firstAck := firstAckedEvent(acked)
	ev, err = createSession(t, final, semanticWriterNamespace, writerSessionName(firstAck.session)).Event(firstAck.seq)
	if err != nil || !bytes.Equal(ev.Payload, writerPayloadAt(payloads, spec.Sessions, firstAck.session, firstAck.seq)) {
		t.Errorf("acked event s%d#%d after second reopen lost or corrupted", firstAck.session, firstAck.seq)
	}
}

// The kill lands a few acks after a plain checkpoint's ack line, so
// acknowledged events exist beyond the checkpoint. After reopen, the
// acknowledged checkpoint must restore: the point-in-time read of
// every session then equals the acknowledged trajectory prefix at the
// moment the checkpoint was acknowledged — the baseline is exactly
// the state the parent watched — and a new append continues from the
// baseline sequence, surviving another reopen.
func TestKill9AckedCheckpointRestoresPointInTimeState(t *testing.T) {
	dir := t.TempDir()
	spec := SemanticWriterSpec{
		Dir:             dir,
		Sessions:        2,
		Events:          60,
		PayloadSize:     256,
		Seed:            12,
		CheckpointEvery: 10,
	}

	// Kill four ack lines past the third checkpoint's ack: events
	// acknowledged after the baseline exist, both on disk and in the
	// acknowledged set, so restore has real state to roll back from.
	stream := semanticAckStream(spec)
	killAfter := ackCountThroughCheckpoint(t, stream, 3) + 4
	acked := killSemanticWriterAfterAcks(t, spec, killAfter)

	st := openStoreAt(t, dir)
	sessions := openWriterSessions(t, st, spec.Sessions)
	payloads := semanticWriterPayloads(spec)

	for _, ack := range acked {
		if ack.kind != 'A' {
			continue
		}
		ev, err := sessions[ack.session].Event(ack.seq)
		if err != nil {
			t.Fatalf("acked event s%d#%d lost after kill -9 and reopen: %v", ack.session, ack.seq, err)
		}
		if want := writerPayloadAt(payloads, spec.Sessions, ack.session, ack.seq); !bytes.Equal(ev.Payload, want) {
			t.Errorf("acked event s%d#%d after kill -9 = %d payload bytes, want the exact acked %d bytes",
				ack.session, ack.seq, len(ev.Payload), len(want))
		}
	}

	cps, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after kill -9 failed: %v", err)
	}
	assertCheckpointPrefixRun(t, cps, acked)

	cpID, snapshot, ok := ackedStateAtLastCheckpoint(acked)
	if !ok {
		t.Fatal("harness observed no acknowledged checkpoint before the kill point")
	}
	assertRestoredPointInTime(t, st, spec, payloads, cpID, snapshot)

	// The restored counter continues from the baseline: the next
	// append takes baseline+1 — overwriting any unacknowledged event
	// that landed there, last write wins — and survives a reopen.
	s0 := createSession(t, st, semanticWriterNamespace, writerSessionName(0))
	resumed := snapshot[0] + 1
	if seq, err := s0.Append([]byte("resumed")); err != nil || seq != resumed {
		t.Fatalf("Append after restore = (%d, %v), want (%d, nil)", seq, err, resumed)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close after restore failed: %v", err)
	}
	final := openStoreAt(t, dir)
	defer closeStore(t, final)
	ev, err := createSession(t, final, semanticWriterNamespace, writerSessionName(0)).Event(resumed)
	if err != nil || string(ev.Payload) != "resumed" {
		t.Errorf("Event(%d) after restore and reopen = %q, %v; want \"resumed\", nil", resumed, ev.Payload, err)
	}
}

// The kill -9 proof of checkpoint batch atomicity: the child commits
// groups of events, each with a staged checkpoint, through one batch;
// the parent kills it mid-run — with the large payloads stretching
// each Commit's append+sync window, the kill can land inside one. The
// reopened store must hold no half group and no half checkpoint: each
// group is either fully visible with its checkpoint listed, or wholly
// invisible with the checkpoint absent — the manifest either complete
// or not on disk at all. An acknowledged group is necessarily fully
// there; the exact landing point is inherently racy, which is
// precisely what is under test.
func TestKill9MidBatchCheckpointNoHalfBatch(t *testing.T) {
	dir := t.TempDir()
	const batchSize = 8
	spec := SemanticWriterSpec{
		Dir:         dir,
		Sessions:    3,
		Events:      21 * batchSize,
		PayloadSize: 16 << 10,
		Seed:        13,
		BatchSize:   batchSize,
	}

	acked := killSemanticWriterAfterAcks(t, spec, 60)

	st := openStoreAt(t, dir)
	sessions := openWriterSessions(t, st, spec.Sessions)
	payloads := semanticWriterPayloads(spec)

	// A torn or half-written manifest would fail the listing itself.
	cps, err := st.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after mid-batch kill failed: %v — a half checkpoint would surface here", err)
	}
	assertCheckpointPrefixRun(t, cps, acked)

	// Each group — its events plus its checkpoint, one engine batch —
	// is all-or-nothing, and the checkpoint is listed exactly when the
	// group's events are visible.
	ackedEvents := ackedEventSet(acked)
	for start, group := 0, 0; start < spec.Events; start, group = start+batchSize, group+1 {
		end := min(start+batchSize, spec.Events)
		visible := 0
		for e := start; e < end; e++ {
			idx := e % spec.Sessions
			seq := uint64(e/spec.Sessions + 1)
			ev, err := sessions[idx].Event(seq)
			switch {
			case err == nil:
				if !bytes.Equal(ev.Payload, payloads[e]) {
					t.Fatalf("event s%d#%d of group %d after mid-batch kill = %d payload bytes, want the exact dataset %d bytes",
						idx, seq, group, len(ev.Payload), len(payloads[e]))
				}
				visible++
			case errors.Is(err, elena.ErrEventNotFound):
				// The group never fully landed — allowed, unless
				// acknowledged.
			default:
				t.Fatalf("Event(s%d#%d) after mid-batch kill err = %v, want hit or ErrEventNotFound", idx, seq, err)
			}
			if ackedEvents[[2]uint64{uint64(idx), seq}] && err != nil {
				t.Fatalf("acknowledged event s%d#%d lost after kill -9: %v", idx, seq, err)
			}
		}
		if visible != 0 && visible != end-start {
			t.Fatalf("half group after kill -9: group %d (events %d..%d) has %d of %d events visible — the group must be all-or-nothing",
				group, start, end, visible, end-start)
		}
		listed := uint64(group+1) <= uint64(len(cps))
		if listed != (visible > 0) {
			t.Fatalf("checkpoint %d listed=%v while its group shows %d/%d events — the group and its baseline must land or vanish together",
				group+1, listed, visible, end-start)
		}
	}

	// The acknowledged checkpoint of the last committed group restores
	// to the acknowledged state at its ack line.
	cpID, snapshot, ok := ackedStateAtLastCheckpoint(acked)
	if !ok {
		t.Fatal("harness observed no acknowledged checkpoint before the kill point")
	}
	assertRestoredPointInTime(t, st, spec, payloads, cpID, snapshot)

	// The recovered store stays writable, and the post-kill write
	// survives another reopen.
	appendTo := createSession(t, st, semanticWriterNamespace, writerSessionName(0))
	if _, err := appendTo.Append([]byte("post-kill")); err != nil {
		t.Fatalf("Append after mid-batch kill and reopen failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close after mid-batch kill failed: %v", err)
	}
	final := openStoreAt(t, dir)
	defer closeStore(t, final)
	events, err := createSession(t, final, semanticWriterNamespace, writerSessionName(0)).Events()
	if err != nil {
		t.Fatalf("Events() after second reopen failed: %v", err)
	}
	if len(events) == 0 || string(events[len(events)-1].Payload) != "post-kill" {
		t.Errorf("last event after second reopen = %q, want \"post-kill\"", events[len(events)-1].Payload)
	}
}

// openStoreAt opens the killed directory anew — the reopen the whole
// harness exists to exercise — and closes it when the test ends.
func openStoreAt(t *testing.T, dir string) *elena.Store {
	t.Helper()
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) after kill -9 failed: %v", dir, err)
	}
	t.Cleanup(func() { closeStore(t, st) })
	return st
}

// openWriterSessions attaches to all of the writer child's sessions
// on st, continuing their stored trajectories.
func openWriterSessions(t *testing.T, st *elena.Store, count int) []*elena.Session {
	t.Helper()
	sessions := make([]*elena.Session, count)
	for i := range sessions {
		sessions[i] = createSession(t, st, semanticWriterNamespace, writerSessionName(i))
	}
	return sessions
}

// writerSessionName returns the child's name for session index i.
func writerSessionName(i int) string {
	return fmt.Sprintf("s%d", i)
}

// writerPayloadAt maps (session index, sequence number) to the
// schedule's payload for that event: event e goes to session
// e%Sessions as its (e/Sessions + 1)-th event.
func writerPayloadAt(payloads [][]byte, sessions, session int, seq uint64) []byte {
	return payloads[(int(seq)-1)*sessions+session]
}

// ackedMaxSequence returns each session's highest acknowledged
// sequence number, zero for sessions without acknowledged events.
func ackedMaxSequence(acked []semanticAck) map[int]uint64 {
	maxSeq := make(map[int]uint64)
	for _, ack := range acked {
		if ack.kind == 'A' && ack.seq > maxSeq[ack.session] {
			maxSeq[ack.session] = ack.seq
		}
	}
	return maxSeq
}

// ackedEventSet returns the acknowledged (session, sequence) pairs.
func ackedEventSet(acked []semanticAck) map[[2]uint64]bool {
	events := make(map[[2]uint64]bool, len(acked))
	for _, ack := range acked {
		if ack.kind == 'A' {
			events[[2]uint64{uint64(ack.session), ack.seq}] = true
		}
	}
	return events
}

// firstAckedEvent returns the first acknowledged event ack, for a
// spot check that survives extra reopens.
func firstAckedEvent(acked []semanticAck) semanticAck {
	for _, ack := range acked {
		if ack.kind == 'A' {
			return ack
		}
	}
	panic("no acknowledged event in the observed acks")
}

// ackCountThroughCheckpoint returns the 1-based ack count up to and
// including the k-th checkpoint ack line of the predicted stream.
func ackCountThroughCheckpoint(t *testing.T, stream []semanticAck, k int) int {
	t.Helper()
	seen := 0
	for i, ack := range stream {
		if ack.kind == 'C' {
			seen++
			if seen == k {
				return i + 1
			}
		}
	}
	t.Fatalf("fixture sanity: the predicted stream holds fewer than %d checkpoints", k)
	return 0
}

// ackedStateAtLastCheckpoint returns the last acknowledged
// checkpoint's ID together with the per-session sequence snapshot of
// the acknowledged events at the moment its ack line appeared — the
// baseline the checkpoint's manifest must record.
func ackedStateAtLastCheckpoint(acked []semanticAck) (uint64, map[int]uint64, bool) {
	counts := make(map[int]uint64)
	var cpID uint64
	var snapshot map[int]uint64
	for _, ack := range acked {
		if ack.kind == 'A' {
			if ack.seq > counts[ack.session] {
				counts[ack.session] = ack.seq
			}
			continue
		}
		cpID = ack.cpID
		snapshot = make(map[int]uint64, len(counts))
		for session, seq := range counts {
			snapshot[session] = seq
		}
	}
	return cpID, snapshot, cpID != 0
}

// assertCheckpointPrefixRun verifies the listed checkpoints form a
// gap-free run of IDs from 1 — plain checkpoints and batch groups both
// land as single engine batches in creation order, so recovery keeps a
// prefix — and that every acknowledged checkpoint is among them.
func assertCheckpointPrefixRun(t *testing.T, cps []elena.CheckpointInfo, acked []semanticAck) {
	t.Helper()
	for i, cp := range cps {
		if cp.ID != uint64(i+1) {
			t.Fatalf("Checkpoints() = IDs %v — the listed checkpoints must form a gap-free run from 1", cps)
		}
	}
	for _, ack := range acked {
		if ack.kind == 'C' && ack.cpID > uint64(len(cps)) {
			t.Fatalf("acknowledged checkpoint %d is missing from the %d listed after kill -9", ack.cpID, len(cps))
		}
	}
}

// assertRestoredPointInTime restores the acknowledged checkpoint cpID
// and verifies the point-in-time read of every session equals the
// acknowledged trajectory prefix the snapshot recorded — events
// byte-exact, endpoint included — which is exactly the state the
// parent watched the child acknowledge before that checkpoint line.
func assertRestoredPointInTime(t *testing.T, st *elena.Store, spec SemanticWriterSpec, payloads [][]byte, cpID uint64, snapshot map[int]uint64) {
	t.Helper()
	if err := st.Restore(cpID); err != nil {
		t.Fatalf("Restore(%d) of an acknowledged checkpoint after kill -9 failed: %v", cpID, err)
	}
	for i := 0; i < spec.Sessions; i++ {
		want := snapshot[i] // a session without acknowledged events reads empty
		events, err := createSession(t, st, semanticWriterNamespace, writerSessionName(i)).EventsUpTo(want)
		if err != nil {
			t.Fatalf("EventsUpTo(%d) of s%d after restore failed: %v", want, i, err)
		}
		if uint64(len(events)) != want {
			t.Fatalf("EventsUpTo(%d) of s%d after restore returned %d events, want %d — the point-in-time read must equal the acknowledged state at the baseline",
				want, i, len(events), want)
		}
		for j, ev := range events {
			if ev.Seq != uint64(j+1) {
				t.Fatalf("EventsUpTo(%d) of s%d holds sequence %d at position %d after restore", want, i, ev.Seq, j+1)
			}
			if payload := writerPayloadAt(payloads, spec.Sessions, i, ev.Seq); !bytes.Equal(ev.Payload, payload) {
				t.Errorf("point-in-time event s%d#%d after restore = %d payload bytes, want the exact acked %d bytes",
					i, ev.Seq, len(ev.Payload), len(payload))
			}
		}
	}
}

//go:build unix

package main

import (
	"fmt"
)

// The full-cycle harness: the W2 acceptance flow — write, checkpoint,
// kill -9, reopen, restore, point-in-time read — as one reproducible
// test. The writes and the kill ride the 06 fault-injection form: a
// reexec of this very test binary is the writer child (see
// main_test.go), it drives the store through the public elena library
// and acks each returned call on stdout, and the parent sends a real
// SIGKILL mid-schedule — no Close hook, no deferred flush, nothing. A
// graceful exit cannot stand in: most durability bugs live exactly in
// the gap between "Append returned" and "Close flushed", and SIGKILL
// removes every line of the process's own shutdown code, so the
// reopened directory holds only what the OS already had.

// Environment variables of the writer-child protocol: the mode switch
// on the child only, the store directory it opens. The skip marker is
// 0001's shared one — a single variable disables every kill -9
// scenario; it is unset by default, so Linux CI runs this flow on
// every push.
const (
	fullCycleModeEnv = "ELENA_TEST_FULLCYCLE_WRITER_CHILD"
	fullCycleDirEnv  = "ELENA_TEST_FULLCYCLE_DIR"
	skipKill9Env     = "ELENA_TEST_SKIP_KILL9"

	// fullCycleNamespace is the scenario's single namespace.
	fullCycleNamespace = "fullcycle"

	// fullCycleSessions is the number of sessions (s0, s1): the
	// checkpoint's manifest must cover every trajectory, so the flow
	// needs more than one.
	fullCycleSessions = 2

	// fullCyclePreEvents is the per-session event count before the
	// checkpoint — the baseline the manifest records for both
	// trajectories.
	fullCyclePreEvents = 3

	// fullCyclePostEvents is the per-session event count after the
	// checkpoint — writes that land past the baseline, acknowledged or
	// not, which the restore's read must refuse to show: the read stops
	// at the recorded sequence, a feature, not a deviation.
	fullCyclePostEvents = 3
)

// fullCycleSessionName returns the child's name for session index s.
func fullCycleSessionName(s int) string {
	return fmt.Sprintf("s%d", s)
}

// fullCyclePayload is the payload of session s's n-th event. Readable
// strings on purpose: the CLI prints them quoted in its exports, the
// test asserts the printed point-in-time read against exactly these
// strings, and the whole flow doubles as the demo's automated script.
func fullCyclePayload(s, n int) string {
	return fmt.Sprintf("%s-note-%02d", fullCycleSessionName(s), n)
}

// fullCycleRounds is the schedule child and parent share: the pre-round
// the checkpoint's baseline covers, and the post-round that lands past
// it. Within a round every session takes one event per number, s0 then
// s1; the checkpoint lands between the rounds and takes ID 1 — the
// store assigns IDs past the highest stored one, and the child creates
// exactly one.
func fullCycleRounds() (pre, post [2]int) {
	return [2]int{1, fullCyclePreEvents},
		[2]int{fullCyclePreEvents + 1, fullCyclePreEvents + fullCyclePostEvents}
}

// fullCycleAck is one ack line of the child's protocol, the 06 wire
// form: kind 'A' carries the session index and sequence number of an
// append that has returned; kind 'C' carries the checkpoint's ID.
type fullCycleAck struct {
	kind    byte
	session int
	seq     uint64
	cpID    uint64
}

// fullCycleAckStream is the complete predicted schedule: the ack of
// every append — printed only after the Append returned — and of the
// one checkpoint, in order. The parent verifies every observed line
// against this prediction, so the child cannot write something other
// than what both sides derived.
func fullCycleAckStream() []fullCycleAck {
	var stream []fullCycleAck
	addRound := func(round [2]int) {
		for n := round[0]; n <= round[1]; n++ {
			for s := 0; s < fullCycleSessions; s++ {
				stream = append(stream, fullCycleAck{kind: 'A', session: s, seq: uint64(n)})
			}
		}
	}
	pre, post := fullCycleRounds()
	addRound(pre)
	stream = append(stream, fullCycleAck{kind: 'C', cpID: 1})
	addRound(post)
	return stream
}

//go:build unix

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"elena"
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
//
// Every observable stage after the kill goes through the CLI seam —
// Run with injected arguments and buffers, the same seam as the
// command tests. The alternative, executing a built binary, would tie
// the test to the toolchain's path (make runs with GO= overrides
// precisely because go need not be on it) and drop the CLI code out of
// the race detector's reach; package main cannot be imported from
// anywhere, so Run-in-process is the seam the spec pinned.

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
	// at the recorded sequence, a feature, not a deviation. The round
	// is deliberately long — dozens of synced appends remain when the
	// parent kills at the checkpoint-plus-one point, the same slack the
	// 06 scenarios keep, so the child is reliably mid-work when the
	// SIGKILL lands instead of racing the signal to a clean exit.
	fullCyclePostEvents = 30
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

// killFullCycleWriterAfter runs the writer child against dir, verifies
// every ack line against the predicted schedule, and sends SIGKILL
// once killAfter lines have been observed — then keeps draining: ack
// lines already in flight when the kill landed still count as
// acknowledged, because the child had watched those calls return
// before dying. killAfter must lie strictly inside (0, total) so the
// kill lands mid-schedule, and the test fails unless the child really
// died from that SIGKILL.
func killFullCycleWriterAfter(t *testing.T, dir string, killAfter int) []fullCycleAck {
	t.Helper()
	if v := os.Getenv(skipKill9Env); v != "" {
		t.Skipf("kill -9 harness disabled via %s=%q", skipKill9Env, v)
	}
	expected := fullCycleAckStream()
	if killAfter < 1 || killAfter >= len(expected) {
		t.Fatalf("killAfter=%d must lie within (0, %d) so the kill lands mid-schedule", killAfter, len(expected))
	}

	stderr := new(bytes.Buffer)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fullCycleModeEnv+"=1", fullCycleDirEnv+"="+dir)
	cmd.Stderr = stderr
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill() // no-op once Wait has reaped it
		}
	})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe writer child stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start writer child: %v", err)
	}

	var acked []fullCycleAck
	var killErr error
	sc := bufio.NewScanner(stdout)
	for i := 0; sc.Scan(); i++ {
		ack := parseFullCycleAck(t, sc.Text())
		if i >= len(expected) {
			t.Fatalf("writer child emitted ack #%d beyond the predicted %d — child and parent schedules diverged", i+1, len(expected))
		}
		if ack != expected[i] {
			t.Fatalf("writer child ack #%d = %+v, want the predicted %+v — child and parent schedules diverged", i+1, ack, expected[i])
		}
		acked = append(acked, ack)
		if len(acked) == killAfter {
			killErr = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading ack lines: %v", err)
	}
	waitFullCycleWriterKilled(t, cmd, killErr, stderr)
	if len(acked) < killAfter {
		t.Fatalf("writer child stdout closed after %d acks, before the kill point %d", len(acked), killAfter)
	}
	return acked
}

// parseFullCycleAck decodes one ack line — "A <session> <seq>" or
// "C <id>" — and fails the test on anything else: the protocol has no
// other vocabulary.
func parseFullCycleAck(t *testing.T, line string) fullCycleAck {
	t.Helper()
	fields := strings.Fields(line)
	switch {
	case len(fields) == 3 && fields[0] == "A":
		session, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("writer child ack %q: bad session: %v", line, err)
		}
		seq, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			t.Fatalf("writer child ack %q: bad sequence: %v", line, err)
		}
		return fullCycleAck{kind: 'A', session: session, seq: seq}
	case len(fields) == 2 && fields[0] == "C":
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("writer child ack %q: bad checkpoint ID: %v", line, err)
		}
		return fullCycleAck{kind: 'C', cpID: id}
	}
	t.Fatalf("writer child emitted malformed ack line %q", line)
	return fullCycleAck{}
}

// waitFullCycleWriterKilled reaps the child and verifies it died from
// the injected SIGKILL. Anything else — a clean exit first among them,
// a different signal, a crash with stderr output — fails the test: a
// run without a real kill -9 proves nothing.
func waitFullCycleWriterKilled(t *testing.T, cmd *exec.Cmd, killErr error, stderr *bytes.Buffer) {
	t.Helper()

	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("writer child exited cleanly; no fault was injected (kill never landed)")
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("writer child wait error: %v\nchild stderr:\n%s", waitErr, stderr)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if killErr != nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("writer child did not die from the injected SIGKILL: wait=%v kill=%v\nchild stderr:\n%s",
			waitErr, killErr, stderr)
	}
}

// TestFullCycleWriteCheckpointKill9RestorePointInTime drives the W2
// acceptance flow through the real fault: a child process writes two
// trajectories and checkpoints between rounds — every call acked on
// stdout — then dies from SIGKILL one acknowledged event past the
// checkpoint. The CLI afterwards reopens the killed directory (status
// shows both sessions), lists the acknowledged baseline, and restores
// it: the printed point-in-time read stops at the recorded sequence
// numbers and shows exactly the acknowledged pre-checkpoint writes —
// never the acknowledged post-baseline write, which stays on disk
// where the baseline's own timeline refuses to look. The library read
// then ties the manifest's baselines to those same payloads, byte for
// byte. Every stage's output format is locked here on a store no
// graceful shutdown ever touched.
func TestFullCycleWriteCheckpointKill9RestorePointInTime(t *testing.T) {
	dir := t.TempDir()

	// Write, checkpoint, kill -9: the kill point is the checkpoint's
	// ack plus one post-baseline event ack, so the baseline is durable,
	// an acknowledged write exists past it, and the racy remainder of
	// the schedule may or may not have landed — the assertions bound
	// that window instead of pinning it.
	killAfter := fullCycleSessions*fullCyclePreEvents + 2
	acked := killFullCycleWriterAfter(t, dir, killAfter)
	if last := acked[killAfter-1]; last.kind != 'A' || last.session != 0 || last.seq != fullCyclePreEvents+1 {
		t.Fatalf("fixture sanity: the kill-point ack = %+v, want the first post-baseline event of s0", last)
	}
	if cp := acked[killAfter-2]; cp.kind != 'C' || cp.cpID != 1 {
		t.Fatalf("fixture sanity: the ack before the kill point = %+v, want checkpoint 1", cp)
	}

	assertFullCycleStatus(t, dir)
	assertFullCycleCheckpointList(t, dir)
	assertFullCycleRestoreRead(t, dir)
	assertFullCycleLibraryRead(t, dir)
}

// assertFullCycleStatus reopens the killed directory through the
// status command — the reopen no graceful shutdown ever touched — and
// locks the overview's format: one namespace, both sessions, and event
// counts bounded below by the acknowledged prefix (an acknowledged
// append is durable, the acked prefix can never shrink) and above by
// the schedule's end (unacknowledged appends may have landed too).
func assertFullCycleStatus(t *testing.T, dir string) {
	t.Helper()
	out := runCLI(t, []string{"status", dir})
	lines := outputLines(t, "status", out, 3+fullCycleSessions)
	wantHeader := fmt.Sprintf("store %s: %d namespaces, %d sessions, %d trajectories",
		dir, 1, fullCycleSessions, fullCycleSessions)
	if lines[0] != wantHeader {
		t.Errorf("status header = %q, want %q", lines[0], wantHeader)
	}
	mergeView := regexp.MustCompile(`^merge view: dead ratio \d+\.\d+%, write amplification \d+\.\d+, data bytes \d+, files \d+$`)
	if !mergeView.MatchString(lines[1]) {
		t.Errorf("status merge view = %q, want the stats line shape", lines[1])
	}
	if lines[2] != "namespace "+fullCycleNamespace {
		t.Errorf("status namespace line = %q, want the scenario's one namespace", lines[2])
	}
	for s := 0; s < fullCycleSessions; s++ {
		shape := regexp.MustCompile(fmt.Sprintf(`^  session %s: perpetual, events (\d+)$`, fullCycleSessionName(s)))
		m := shape.FindStringSubmatch(lines[3+s])
		if m == nil {
			t.Errorf("status session line = %q, want the session line shape", lines[3+s])
			continue
		}
		events, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("status session line %q: bad event count: %v", lines[3+s], err)
		}
		acked := fullCyclePreEvents // everything acknowledged before the checkpoint
		if s == 0 {
			acked++ // s0's first post-baseline event was acknowledged before the kill
		}
		if events < acked || events > fullCyclePreEvents+fullCyclePostEvents {
			t.Errorf("session %s shows %d events, want between the acknowledged %d and the schedule's %d",
				fullCycleSessionName(s), events, acked, fullCyclePreEvents+fullCyclePostEvents)
		}
	}
	assertSignalLines(t, out)
}

// assertFullCycleCheckpointList pins the checkpoint listing of the
// killed store: exactly one checkpoint — the acknowledged one, never a
// half manifest — under ID 1.
func assertFullCycleCheckpointList(t *testing.T, dir string) {
	t.Helper()
	out := runCLI(t, []string{"checkpoint", "list", dir})
	lines := outputLines(t, "checkpoint list", out, 2)
	if lines[0] != "checkpoints stored: 1" {
		t.Errorf("checkpoint list header = %q, want %q", lines[0], "checkpoints stored: 1")
	}
	created := regexp.MustCompile(`^  checkpoint 1 created \S+$`)
	if !created.MatchString(lines[1]) {
		t.Errorf("checkpoint list line = %q, want the checkpoint 1 line with its creation time", lines[1])
	}
	assertSignalLines(t, out)
}

// assertFullCycleRestoreRead runs the restore and locks the printed
// point-in-time read line by line: the restored header, one trajectory
// line per session at the recorded baseline — the pre-checkpoint seq —
// each with its three acknowledged events in order, payloads quoted,
// and the boundary line. The acknowledged post-baseline writes appear
// nowhere in the read: it stops at the recorded sequence, which is the
// feature this flow exists to demonstrate. The event lines are matched
// against the schedule's own payloads with only the timestamp left to
// \S+ — RFC3339Nano has no spaces — so the formats regress-lock here.
func assertFullCycleRestoreRead(t *testing.T, dir string) {
	t.Helper()
	out := runCLI(t, []string{"restore", dir, "1"})
	want := []string{
		`^checkpoint 1 \(created \S+\) restored$`,
		`^point-in-time read at the baseline:$`,
	}
	for s := 0; s < fullCycleSessions; s++ {
		want = append(want, fmt.Sprintf(`^trajectory %s/%s to seq %d$`,
			fullCycleNamespace, fullCycleSessionName(s), fullCyclePreEvents))
		for n := 1; n <= fullCyclePreEvents; n++ {
			payload := fullCyclePayload(s, n)
			want = append(want, fmt.Sprintf(`^  event #%d  \S+  %d bytes  %q$`, n, len(payload), payload))
		}
	}
	want = append(want, `^restore read stops at each baseline; events past it stay on disk$`)

	lines := outputLines(t, "restore", out, len(want))
	for i, pattern := range want {
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("fixture sanity: bad pattern %q: %v", pattern, err)
		}
		if !re.MatchString(lines[i]) {
			t.Errorf("restore line %d = %q, want match %q", i+1, lines[i], pattern)
		}
	}
	assertSignalLines(t, out)
}

// assertFullCycleLibraryRead ties the flow's data axis to the
// acknowledged writes: through the library, the restored checkpoint's
// manifest records the pre-checkpoint seq on both trajectories with
// perpetual deadlines, the point-in-time read at that baseline returns
// exactly the pre-checkpoint payloads byte for byte, and the
// acknowledged post-baseline write is still on disk — a restore reads
// to a baseline, it deletes nothing.
func assertFullCycleLibraryRead(t *testing.T, dir string) {
	t.Helper()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open the killed directory through the library: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close after the library read: %v", err)
		}
	})

	checkpoints, err := store.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints() after kill -9: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != 1 {
		t.Fatalf("Checkpoints() = %v, want exactly the acknowledged checkpoint 1", checkpoints)
	}
	manifest, err := store.Checkpoint(1)
	if err != nil {
		t.Fatalf("Checkpoint(1) after kill -9: %v", err)
	}
	if len(manifest.Trajectories) != fullCycleSessions {
		t.Fatalf("manifest records %d trajectories, want the scenario's %d", len(manifest.Trajectories), fullCycleSessions)
	}
	for s, baseline := range manifest.Trajectories {
		if baseline.Namespace != fullCycleNamespace || baseline.Session != fullCycleSessionName(s) {
			t.Fatalf("manifest trajectory %d = %s/%s, want %s/%s", s, baseline.Namespace, baseline.Session, fullCycleNamespace, fullCycleSessionName(s))
		}
		if baseline.ReachedSeq != fullCyclePreEvents {
			t.Fatalf("manifest baseline of %s = seq %d, want the pre-checkpoint %d", baseline.Session, baseline.ReachedSeq, fullCyclePreEvents)
		}
		if !baseline.Deadline.IsZero() {
			t.Fatalf("manifest deadline of %s = %v, want the perpetual zero time", baseline.Session, baseline.Deadline)
		}
	}

	for s := 0; s < fullCycleSessions; s++ {
		sess, err := store.CreateSession(fullCycleNamespace, fullCycleSessionName(s))
		if err != nil {
			t.Fatalf("attach to %s after kill -9: %v", fullCycleSessionName(s), err)
		}
		events, err := sess.EventsUpTo(fullCyclePreEvents)
		if err != nil {
			t.Fatalf("EventsUpTo(%d) of %s: %v", fullCyclePreEvents, fullCycleSessionName(s), err)
		}
		if len(events) != fullCyclePreEvents {
			t.Fatalf("EventsUpTo(%d) of %s returned %d events, want the acknowledged %d",
				fullCyclePreEvents, fullCycleSessionName(s), len(events), fullCyclePreEvents)
		}
		for n, ev := range events {
			if ev.Seq != uint64(n+1) || string(ev.Payload) != fullCyclePayload(s, n+1) {
				t.Fatalf("point-in-time event %d of %s = #%d %q, want #%d %q — the baseline read must equal the acknowledged pre-checkpoint writes",
					n+1, fullCycleSessionName(s), ev.Seq, ev.Payload, n+1, fullCyclePayload(s, n+1))
			}
		}
	}

	// The acknowledged post-baseline write stays on disk; only the
	// baseline's read refuses to see it.
	s0, err := store.CreateSession(fullCycleNamespace, fullCycleSessionName(0))
	if err != nil {
		t.Fatalf("attach to s0 after kill -9: %v", err)
	}
	past, err := s0.Event(fullCyclePreEvents + 1)
	if err != nil || string(past.Payload) != fullCyclePayload(0, fullCyclePreEvents+1) {
		t.Fatalf("Event(%d) of s0 = %q, %v — the acknowledged post-baseline write must stay on disk",
			fullCyclePreEvents+1, past.Payload, err)
	}
}

// runCLI runs one stage through the Run seam — injected arguments and
// buffers — and returns the captured stdout, failing on any non-zero
// exit with the stderr verbatim.
func runCLI(t *testing.T, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(%v) exit code = %d, want 0\nstderr:\n%s", args, code, stderr.String())
	}
	return stdout.String()
}

// outputLines splits one stage's output into its exact line count,
// failing loudly on any surprise: the stages' formats are regression
// locks, not approximations.
func outputLines(t *testing.T, stage, out string, want int) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != want {
		t.Fatalf("%s output holds %d lines, want exactly %d:\n%s", stage, len(lines), want, out)
	}
	return lines
}

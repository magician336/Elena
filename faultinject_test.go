//go:build unix

package elena_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Fault-injection harness: the semantic layer's kill -9 seam, rebuilt
// from 0001's engine harness in the same shape (the engine harness
// files are engine-package tests and cannot be imported — this is a
// re-creation of the pattern, not a reuse of the code).
//
// Why a really killed process instead of "open the directory again
// without Close"? Because most durability bugs live exactly in the gap
// between "Append returned" and "Close flushed": a normal test process
// always reaches Close, so userspace buffers, lazy flushes, and
// memory-only state are silently laundered before any reopen. SIGKILL
// removes every line of the process's own shutdown code — no Close
// hook, no deferred flush, nothing — so the reopened directory holds
// only what the OS already had. If an acknowledged append or
// checkpoint ever existed only inside the process, this harness loses
// it and the test fails. The same honest fault-model boundary as the
// engine harness applies: SIGKILL kills the process, not the machine;
// surviving it proves the process-crash axis of the semantic layer's
// "acknowledged means durable" claim, not power-loss behavior.
//
// The writer child is a reexec of this very test binary: TestMain (in
// main_test.go) detects the writer-child environment variable and
// switches the process into writer mode before any test runs, so the
// child drives the exact same compiled elena library — including race
// instrumentation when the suite runs under -race — through its public
// API alone.

// Environment variable names for the writer-child protocol. The mode
// variable is set on the child only; the rest carry the
// SemanticWriterSpec. The skip marker is 0001's, shared with the
// engine harness: one variable disables every kill -9 scenario.
const (
	semanticWriterModeEnv       = "ELENA_TEST_SEMANTIC_WRITER_CHILD"
	semanticWriterDirEnv        = "ELENA_TEST_SEMANTIC_WRITER_DIR"
	semanticWriterSessionsEnv   = "ELENA_TEST_SEMANTIC_WRITER_SESSIONS"
	semanticWriterEventsEnv     = "ELENA_TEST_SEMANTIC_WRITER_EVENTS"
	semanticWriterSeedEnv       = "ELENA_TEST_SEMANTIC_WRITER_SEED"
	semanticWriterPayloadEnv    = "ELENA_TEST_SEMANTIC_WRITER_PAYLOAD_SIZE"
	semanticWriterBatchEnv      = "ELENA_TEST_SEMANTIC_WRITER_BATCH_SIZE"
	semanticWriterCheckpointEnv = "ELENA_TEST_SEMANTIC_WRITER_CHECKPOINT_EVERY"
	skipKill9Env                = "ELENA_TEST_SKIP_KILL9"

	// semanticWriterNamespace is the one namespace the child writes
	// into; its sessions are s0..s{Sessions-1}.
	semanticWriterNamespace = "kill9"

	// semanticWriterStreamPCG is the fixed PCG stream id of the
	// dataset ("elena", the engine harness's id): the seed alone
	// selects the payloads.
	semanticWriterStreamPCG = 0x656c656e61
)

// SemanticWriterSpec configures one child-writer run over the public
// elena API. The payloads are derived deterministically from the seed
// on both sides of the process boundary, and the write schedule is
// shared arithmetic, so the parent predicts every ack line the child
// emits and verifies acknowledged state without trusting the child
// beyond the ack ordering.
type SemanticWriterSpec struct {
	// Dir is the store directory the child opens.
	Dir string

	// Sessions is the number of sessions the child creates in the
	// namespace before writing. Schedule event e belongs to session
	// e%Sessions and is that session's (e/Sessions + 1)-th event, so
	// sequence numbers are derivable from the schedule position alone.
	Sessions int

	// Events is the total number of events of the schedule.
	Events int

	// PayloadSize is the exact payload length of every event, in bytes.
	PayloadSize int

	// Seed selects the deterministic event payloads.
	Seed int64

	// CheckpointEvery governs plain mode (BatchSize == 0): after every
	// CheckpointEvery acknowledged events the child creates one plain
	// checkpoint and acks its ID. It must be positive in plain mode.
	CheckpointEvery int

	// BatchSize, when > 0, switches the child into batch mode: every
	// BatchSize consecutive events plus one staged checkpoint go
	// through a single NewBatch/Append/Checkpoint/Commit, and the ack
	// lines of the whole group print only after Commit returned — so
	// an observed ack of the group implies the events and the
	// checkpoint became durable together. The window inside a Commit
	// is where a SIGKILL leaves a torn or absent group that recovery
	// must resolve to all-or-nothing.
	BatchSize int
}

// semanticWriterPayloads returns the deterministic event payloads in
// schedule order: payload e belongs to session e%Sessions. Child and
// parent derive the same bytes from the same seed, so a corrupted
// payload can never be explained away as "the child wrote something
// else".
func semanticWriterPayloads(spec SemanticWriterSpec) [][]byte {
	rng := rand.New(rand.NewPCG(uint64(spec.Seed), semanticWriterStreamPCG))
	payloads := make([][]byte, spec.Events)
	for e := range payloads {
		payload := make([]byte, spec.PayloadSize)
		for i := range payload {
			payload[i] = byte(rng.UintN(256))
		}
		payloads[e] = payload
	}
	return payloads
}

// semanticAck is one ack line of the child's protocol: kind 'A'
// carries the session index and sequence number of an append that has
// returned; kind 'C' carries the ID of a checkpoint that is stored.
type semanticAck struct {
	kind    byte
	session int
	seq     uint64
	cpID    uint64
}

// semanticAckStream returns the full ack stream the child emits for
// spec, in order: one 'A' line per appended event — printed only after
// the Append or batch Commit returned — and one 'C' line per
// checkpoint, in plain mode after every CheckpointEvery events, in
// batch mode once per committed group. Checkpoint IDs are assigned by
// the store as one past the highest stored ID and the child creates
// them serially, so the k-th checkpoint of the run takes ID k — the
// prediction both sides share.
func semanticAckStream(spec SemanticWriterSpec) []semanticAck {
	var stream []semanticAck
	nextCp := uint64(0)
	addEvent := func(e int) {
		stream = append(stream, semanticAck{kind: 'A', session: e % spec.Sessions, seq: uint64(e/spec.Sessions + 1)})
	}
	addCheckpoint := func() {
		nextCp++
		stream = append(stream, semanticAck{kind: 'C', cpID: nextCp})
	}
	if spec.BatchSize > 0 {
		for start := 0; start < spec.Events; start += spec.BatchSize {
			end := min(start+spec.BatchSize, spec.Events)
			for e := start; e < end; e++ {
				addEvent(e)
			}
			addCheckpoint()
		}
		return stream
	}
	for e := 0; e < spec.Events; e++ {
		addEvent(e)
		if (e+1)%spec.CheckpointEvery == 0 {
			addCheckpoint()
		}
	}
	return stream
}

// killSemanticWriterAfterAcks runs the child writer for spec and
// returns every ack line the parent observed. The child reexecutes
// this test binary in writer mode, drives the store through the public
// elena API, and prints one ack line per call that has already
// returned. When the killAfter-th ack line has been read, the parent
// sends SIGKILL and keeps draining; ack lines already in flight when
// the kill landed still count as acknowledged, because the child had
// watched those calls return before dying. The test fails unless the
// child really died from that SIGKILL. killAfter must lie strictly
// inside (0, total ack count) so the kill lands mid-run.
func killSemanticWriterAfterAcks(t *testing.T, spec SemanticWriterSpec, killAfter int) []semanticAck {
	t.Helper()
	maybeSkipSemanticKill9(t)
	if err := validateSemanticWriterSpec(spec); err != nil {
		t.Fatalf("invalid writer spec: %v", err)
	}
	expected := semanticAckStream(spec)
	if killAfter < 1 || killAfter >= len(expected) {
		t.Fatalf("killAfter=%d must lie within (0, %d) so the kill lands mid-run", killAfter, len(expected))
	}

	stderr := new(bytes.Buffer)
	cmd := exec.Command(os.Args[0])
	cmd.Env = semanticWriterChildEnv(spec)
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

	var acked []semanticAck
	var killErr error
	sc := bufio.NewScanner(stdout)
	for i := 0; sc.Scan(); i++ {
		ack := parseSemanticAck(t, sc.Text())
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
	waitSemanticWriterKilled(t, cmd, killErr, stderr)
	if len(acked) < killAfter {
		t.Fatalf("writer child stdout closed after %d acks, before the kill point %d", len(acked), killAfter)
	}
	return acked
}

// maybeSkipSemanticKill9 skips the harness when the environment cannot
// run child processes, through 0001's shared skip marker: the engine
// and semantic kill -9 scenarios are enabled or disabled together. The
// variable is deliberately unset by default — this machine and the
// Linux CI both run every kill -9 scenario on each invocation.
func maybeSkipSemanticKill9(t *testing.T) {
	t.Helper()
	if v := os.Getenv(skipKill9Env); v != "" {
		t.Skipf("kill -9 harness disabled via %s=%q", skipKill9Env, v)
	}
}

// validateSemanticWriterSpec rejects specs the child or the prediction
// cannot run with.
func validateSemanticWriterSpec(spec SemanticWriterSpec) error {
	switch {
	case spec.Dir == "":
		return fmt.Errorf("spec without a directory: %+v", spec)
	case spec.Sessions <= 0:
		return fmt.Errorf("spec without sessions: %+v", spec)
	case spec.Events <= 0:
		return fmt.Errorf("spec without events: %+v", spec)
	case spec.PayloadSize <= 0:
		return fmt.Errorf("spec without a payload size: %+v", spec)
	case spec.BatchSize == 0 && spec.CheckpointEvery <= 0:
		return fmt.Errorf("plain-mode spec without CheckpointEvery: %+v", spec)
	default:
		return nil
	}
}

// semanticWriterChildEnv returns the child environment: the parent's
// plus the encoded spec and the mode switch.
func semanticWriterChildEnv(spec SemanticWriterSpec) []string {
	return append(os.Environ(),
		semanticWriterModeEnv+"=1",
		semanticWriterDirEnv+"="+spec.Dir,
		semanticWriterSessionsEnv+"="+strconv.Itoa(spec.Sessions),
		semanticWriterEventsEnv+"="+strconv.Itoa(spec.Events),
		semanticWriterSeedEnv+"="+strconv.FormatInt(spec.Seed, 10),
		semanticWriterPayloadEnv+"="+strconv.Itoa(spec.PayloadSize),
		semanticWriterBatchEnv+"="+strconv.Itoa(spec.BatchSize),
		semanticWriterCheckpointEnv+"="+strconv.Itoa(spec.CheckpointEvery),
	)
}

// parseSemanticAck decodes one ack line: "A <session> <seq>" or
// "C <id>". Anything else fails the test — the protocol has no other
// vocabulary.
func parseSemanticAck(t *testing.T, line string) semanticAck {
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
		return semanticAck{kind: 'A', session: session, seq: seq}
	case len(fields) == 2 && fields[0] == "C":
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("writer child ack %q: bad checkpoint ID: %v", line, err)
		}
		return semanticAck{kind: 'C', cpID: id}
	}
	t.Fatalf("writer child emitted malformed ack line %q", line)
	return semanticAck{}
}

// waitSemanticWriterKilled reaps the child and verifies it died from
// the injected SIGKILL. Anything else — a clean exit, a different
// signal, a crash with stderr output — fails the test: a scenario
// without a real kill -9 proves nothing.
func waitSemanticWriterKilled(t *testing.T, cmd *exec.Cmd, killErr error, stderr *bytes.Buffer) {
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

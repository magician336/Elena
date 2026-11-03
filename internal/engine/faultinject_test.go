//go:build unix

package engine_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// Fault-injection harness: the kill -9 seam.
//
// Why a really killed process instead of "open the directory again
// without Close"? Because most durability bugs live exactly in the gap
// between "Put returned" and "Close flushed": a normal test process
// always reaches Close, so userspace write buffers, lazy flushes, and
// index-only updates are silently laundered before any reopen. SIGKILL
// removes every line of the process's own shutdown code — no Close
// hook, no deferred flush, nothing — so the reopened directory holds
// only what the OS already had. If an acknowledged Put ever existed
// only inside the process, this harness loses it and the test fails.
// Today the engine writes straight through os.File plus fdatasync, so
// the harness is regression armor: anyone reintroducing buffering or
// "flush on Close" loses acked writes here immediately.
//
// The honest boundary of this fault model (fsync semantics): SIGKILL
// kills the process, not the machine. Dirty page-cache pages of a dead
// process are still written back by the kernel, so surviving SIGKILL
// does not by itself prove fdatasync discipline — a lost-machine
// (power-loss) fault needs lower-level tooling and is out of scope.
// What this harness proves is the process-crash axis of the
// "acknowledged writes are durable" claim: no exit-time code runs, the
// file lock dies with the process, the data files end with whatever
// tail the kernel really kept, and recovery must handle all of it.
//
// The writer child is a reexec of this very test binary: TestMain (in
// main_test.go) detects the writer-child environment variable and
// switches the process into writer mode before any test runs. That
// way the harness needs no separately built helper program, and the
// child exercises the exact same compiled engine — including race
// instrumentation when the suite runs under -race.

// Environment variable names for the writer-child protocol. The mode
// variable is set on the child only; the rest carry the WriterSpec.
const (
	writerChildModeEnv     = "ELENA_TEST_WRITER_CHILD"
	writerDirEnv           = "ELENA_TEST_WRITER_DIR"
	writerWritesEnv        = "ELENA_TEST_WRITER_WRITES"
	writerSeedEnv          = "ELENA_TEST_WRITER_SEED"
	writerValueSizeEnv     = "ELENA_TEST_WRITER_VALUE_SIZE"
	writerRotateEveryEnv   = "ELENA_TEST_WRITER_ROTATE_EVERY"
	skipKill9Env           = "ELENA_TEST_SKIP_KILL9"
	writerDatasetStreamPCG = 0x656c656e61 // fixed PCG stream id ("elena"): a seed fully determines the dataset
)

// WriterSpec configures one child-writer run. The dataset is derived
// deterministically from the spec on both sides of the process
// boundary, so the parent can verify exact values without trusting the
// child's stdout beyond the ack ordering.
type WriterSpec struct {
	// Dir is the storage directory the child opens.
	Dir string

	// Writes is the number of puts the child attempts before exiting
	// cleanly (when the parent kills it first, fewer happen).
	Writes int

	// ValueSize is the exact value length of every put, in bytes.
	ValueSize int

	// Seed selects the deterministic key/value dataset.
	Seed int64

	// RotateEvery, when > 0, asks the engine to rotate the active data
	// file after roughly every RotateEvery acknowledged puts: the child
	// derives a MaxDataFileSize from the dataset's fixed entry size so
	// real engine rotation spreads the writes across several data
	// files, each sealed with a hint file. The kill can then land
	// mid-rotation, mid-hint-write, or anywhere between files.
	RotateEvery int
}

// AckedWrite is one write of a child-writer run: its key and the exact
// value the child put. The name comes from the ack protocol, but the
// same type also enumerates writes before any ack is observed.
type AckedWrite struct {
	Key   string
	Value []byte
}

// WriterDataset returns the deterministic sequence of writes a child
// writer performs for spec. Both the child (to write) and the parent
// (to know the expected values) derive it independently from the same
// seed, so a corrupted value can never be explained away as "the child
// wrote something else".
func WriterDataset(spec WriterSpec) []AckedWrite {
	rng := rand.New(rand.NewPCG(uint64(spec.Seed), writerDatasetStreamPCG))
	writes := make([]AckedWrite, 0, spec.Writes)
	for i := 0; i < spec.Writes; i++ {
		value := make([]byte, spec.ValueSize)
		for j := range value {
			value[j] = byte(rng.UintN(256))
		}
		writes = append(writes, AckedWrite{
			Key:   fmt.Sprintf("%04x-key-%06d", rng.UintN(0x10000), i),
			Value: value,
		})
	}
	return writes
}

// KillWriterAfterAcks runs a child writer per spec and returns every
// write whose ack line the parent observed. The child reexecutes this
// test binary in writer mode, puts the dataset entries one by one
// through the public engine API, and prints one ack line per Put that
// has already returned. As soon as the killAfter-th ack line is read,
// the parent sends SIGKILL and waits for the child to die; ack lines
// already in flight when the kill landed still count as acknowledged,
// because the child had returned those Puts before dying.
//
// The test fails unless the child really died from that SIGKILL: a
// clean exit means no fault was injected and the scenario is void.
// killAfter must lie strictly between 0 and spec.Writes so the kill
// lands mid-run.
func KillWriterAfterAcks(t *testing.T, spec WriterSpec, killAfter int) []AckedWrite {
	t.Helper()
	maybeSkipKill9(t)
	if spec.Writes <= 0 || spec.ValueSize <= 0 {
		t.Fatalf("invalid writer spec: %+v", spec)
	}
	if killAfter < 1 || killAfter >= spec.Writes {
		t.Fatalf("killAfter=%d must lie within (0, Writes=%d) so the kill lands mid-run", killAfter, spec.Writes)
	}

	dataset := WriterDataset(spec)
	byKey := make(map[string]AckedWrite, len(dataset))
	for _, w := range dataset {
		byKey[w.Key] = w
	}

	cmd, stdout, stderr := startWriterChild(t, spec)
	acked, killErr := observeAcksAndKill(t, cmd, stdout, byKey, killAfter)
	waitKilledBySIGKILL(t, cmd, killErr, stderr)
	return acked
}

// maybeSkipKill9 skips the harness when the environment cannot run
// child processes (some sandboxes forbid fork/exec or signalling).
// The variable is deliberately unset by default: this machine and the
// Linux CI both run the kill -9 scenarios on every invocation.
func maybeSkipKill9(t *testing.T) {
	t.Helper()
	if v := os.Getenv(skipKill9Env); v != "" {
		t.Skipf("kill -9 harness disabled via %s=%q", skipKill9Env, v)
	}
}

// startWriterChild spawns the reexec writer child for spec, wires its
// stdout to a pipe and its stderr to a buffer, and registers a cleanup
// that kills it if the test dies first.
func startWriterChild(t *testing.T, spec WriterSpec) (*exec.Cmd, io.ReadCloser, *bytes.Buffer) {
	t.Helper()

	stderr := new(bytes.Buffer)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		writerChildModeEnv+"=1",
		writerDirEnv+"="+spec.Dir,
		writerWritesEnv+"="+strconv.Itoa(spec.Writes),
		writerSeedEnv+"="+strconv.FormatInt(spec.Seed, 10),
		writerValueSizeEnv+"="+strconv.Itoa(spec.ValueSize),
		writerRotateEveryEnv+"="+strconv.Itoa(spec.RotateEvery),
	)
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
	return cmd, stdout, stderr
}

// observeAcksAndKill reads ack lines from the child's stdout and sends
// SIGKILL the moment the killAfter-th ack arrives. It keeps draining
// until the pipe closes, so acks that were already in the pipe when
// the kill landed are still observed: the child had returned those
// Puts before dying, hence they count as acknowledged.
func observeAcksAndKill(t *testing.T, cmd *exec.Cmd, stdout io.Reader, byKey map[string]AckedWrite, killAfter int) ([]AckedWrite, error) {
	t.Helper()

	var acked []AckedWrite
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		w, ok := byKey[sc.Text()]
		if !ok {
			t.Fatalf("writer child acked unknown key %q", sc.Text())
		}
		acked = append(acked, w)
		if len(acked) == killAfter {
			return acked, syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading ack lines: %v", err)
	}
	// The pipe closed before the kill point: the child cannot have
	// finished all writes (killAfter < Writes), so it died on its own.
	t.Fatalf("writer child stdout closed after %d acks, before the kill point %d", len(acked), killAfter)
	return nil, nil // unreachable; Fatalf terminates the test
}

// waitKilledBySIGKILL reaps the child and verifies it died from the
// injected SIGKILL. Anything else — a clean exit, a different signal,
// a crash with stderr output — fails the test: a scenario without a
// real kill -9 proves nothing.
func waitKilledBySIGKILL(t *testing.T, cmd *exec.Cmd, killErr error, stderr *bytes.Buffer) {
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

// writerRotateThreshold returns the MaxDataFileSize that makes the
// engine rotate after about spec.RotateEvery puts. The dataset's keys
// all have the same length ("0000-key-000000" = 15 bytes) and values
// share spec.ValueSize, so every entry encodes to the same footprint
// and the threshold maps cleanly onto a put count.
func writerRotateThreshold(spec WriterSpec) int {
	const writerKeySize = 15
	entrySize := 28 + writerKeySize + spec.ValueSize // header + key + value
	return spec.RotateEvery * entrySize
}

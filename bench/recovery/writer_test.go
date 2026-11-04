package recovery

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"elena"
)

// writerChildSpec is one small schedule the writer tests run in-process.
func writerChildSpec(dir string) Spec {
	return Spec{Dir: dir, Sessions: 2, Events: 8, PayloadSize: 64, Seed: 3}
}

// TestAppendSchedule pins the writer loop against the public elena API:
// every schedule append acknowledges exactly the predicted stream —
// session, sequence, and order — on a real store.
func TestAppendSchedule(t *testing.T) {
	dir := t.TempDir()
	spec := writerChildSpec(dir)

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	var acks []Ack
	err = appendSchedule(st, spec, func(ack Ack) error {
		acks = append(acks, ack)
		return nil
	})
	cErr := st.Close()
	if err != nil || cErr != nil {
		t.Fatalf("appendSchedule=%v close=%v", err, cErr)
	}
	want := AckStream(spec)
	if len(acks) != len(want) {
		t.Fatalf("appendSchedule acknowledged %d events, want %d", len(acks), len(want))
	}
	for i := range want {
		if acks[i] != want[i] {
			t.Fatalf("ack %d = %+v, want the predicted %+v", i, acks[i], want[i])
		}
	}
}

// TestAppendScheduleFailsClosedStore pins that the loop surfaces store
// errors instead of acknowledging through them.
func TestAppendScheduleFailsClosedStore(t *testing.T) {
	dir := t.TempDir()
	spec := writerChildSpec(dir)
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = appendSchedule(st, spec, func(Ack) error { return nil })
	if err == nil {
		t.Fatal("appendSchedule on a closed store succeeded, want an error")
	}
}

// TestRunWriterChild pins the child main end to end in-process: the
// spec arrives through the environment, the schedule lands in a real
// store, stdout carries the predicted ack lines plus the hold marker,
// and a closed stdin — a vanished parent — is a failure exit.
func TestRunWriterChild(t *testing.T) {
	dir := t.TempDir()
	spec := writerChildSpec(dir)
	t.Setenv(writerChildModeEnv, "1")
	t.Setenv(writerChildDirEnv, spec.Dir)
	t.Setenv(writerChildSessionsEnv, "2")
	t.Setenv(writerChildEventsEnv, "8")
	t.Setenv(writerChildSeedEnv, "3")
	t.Setenv(writerChildPayloadEnv, "64")

	var stdout, stderr bytes.Buffer
	code := RunWriterChild(strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("RunWriterChild with a closed stdin exited %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "stdin") {
		t.Fatalf("child stderr = %q, want the vanished-parent note", stderr.String())
	}

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != spec.Events+1 {
		t.Fatalf("child emitted %d lines, want %d acks plus the hold marker", len(lines), spec.Events)
	}
	want := AckStream(spec)
	for i, ack := range want {
		if lines[i] != fmt.Sprintf("A %d %d", ack.Session, ack.Seq) {
			t.Fatalf("child line %d = %q, want the ack of %+v", i, lines[i], ack)
		}
	}
	if lines[spec.Events] != fmt.Sprintf("W %d", spec.Events) {
		t.Fatalf("child hold marker = %q, want %q", lines[spec.Events], fmt.Sprintf("W %d", spec.Events))
	}
	// No reopen here on purpose: the child main never closes its store
	// — crash purity — so in-process the directory stays locked until
	// the test process exits. The rig's end-to-end test owns the
	// reopen assertions after a real kill.
}

// TestRunWriterChildRejectsBadEnvironment pins the environment
// contract: a child started without a decodable spec fails loudly
// instead of writing anything.
func TestRunWriterChildRejectsBadEnvironment(t *testing.T) {
	t.Setenv(writerChildModeEnv, "1")
	t.Setenv(writerChildDirEnv, "/tmp/rig-bad-env")
	// Sessions and events unset: the decode must fail on the first
	// missing value.
	var stdout, stderr bytes.Buffer
	code := RunWriterChild(strings.NewReader(""), &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("RunWriterChild with a broken env = (%d, %q), want (1, no output)", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), writerChildSessionsEnv) {
		t.Fatalf("child stderr = %q, want the missing %s named", stderr.String(), writerChildSessionsEnv)
	}
}

// TestWriterChildEnvRoundTrip pins that the encoded environment decodes
// back to the exact spec — the child and parent agree on the payload of
// the protocol.
func TestWriterChildEnvRoundTrip(t *testing.T) {
	spec := Spec{Dir: "/tmp/rig-env", Sessions: 8, Events: 4096, PayloadSize: 1024, Seed: 99}
	for _, kv := range writerChildEnv(spec) {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	decoded, err := writerSpecFromEnv()
	if err != nil {
		t.Fatalf("writerSpecFromEnv: %v", err)
	}
	if decoded != spec {
		t.Fatalf("env round trip = %+v, want %+v", decoded, spec)
	}
}

// TestKill9Disabled pins the shared skip marker: the rig's kill -9
// scenarios disable through 0001's variable — the same one every other
// kill -9 suite of the repository honors — and only when it is set.
func TestKill9Disabled(t *testing.T) {
	if kill9Disabled(func(string) string { return "" }) {
		t.Fatal("kill -9 disabled with the marker unset")
	}
	if !kill9Disabled(func(string) string { return "yes" }) {
		t.Fatal("kill -9 enabled with the marker set")
	}
	if os.Getenv(skipKill9Env) != "" {
		t.Skipf("kill -9 harness disabled via %s", skipKill9Env)
	}
}

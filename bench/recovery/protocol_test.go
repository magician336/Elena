package recovery

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// scanSpec is a small schedule for protocol-level tests: 6 events over
// 3 sessions, so the predicted stream is A 0 1, A 1 1, A 2 1, A 0 2,
// A 1 2, A 2 2.
func scanSpec(killAfterAcks int) Spec {
	return Spec{Dir: "/tmp/rig-protocol", Sessions: 3, Events: 6, PayloadSize: 16, Seed: 1,
		KillAfterAcks: killAfterAcks}
}

// happyStream renders the predicted ack lines plus the hold marker.
func happyStream(spec Spec) string {
	var b strings.Builder
	for _, ack := range AckStream(spec) {
		fmt.Fprintf(&b, "A %d %d\n", ack.Session, ack.Seq)
	}
	fmt.Fprintf(&b, "W %d\n", spec.Events)
	return b.String()
}

// TestScanAcksHoldKill pins the hold-kill observation: every predicted
// ack line counts, the hold marker agrees with the schedule, and the
// kill fires exactly once — at the marker, never at an ack.
func TestScanAcksHoldKill(t *testing.T) {
	spec := scanSpec(0)
	var kills []string
	acked, err := scanAcks(strings.NewReader(happyStream(spec)), spec, func() { kills = append(kills, "x") })
	if err != nil {
		t.Fatalf("scanAcks of the happy stream: %v", err)
	}
	if len(acked) != spec.Events {
		t.Fatalf("scanAcks observed %d acks, want %d", len(acked), spec.Events)
	}
	if len(kills) != 1 {
		t.Fatalf("kill fired %d times, want once at the hold marker", len(kills))
	}
}

// TestScanAcksMidRunKill pins the mid-run observation: the kill fires
// at the n-th ack, later lines — including the hold marker — are still
// observed and validated, and no second kill fires.
func TestScanAcksMidRunKill(t *testing.T) {
	spec := scanSpec(3)
	var kills int
	acked, err := scanAcks(strings.NewReader(happyStream(spec)), spec, func() { kills++ })
	if err != nil {
		t.Fatalf("scanAcks of the mid-run stream: %v", err)
	}
	if kills != 1 {
		t.Fatalf("kill fired %d times, want once at ack 3", kills)
	}
	if len(acked) != spec.Events {
		t.Fatalf("scanAcks observed %d acks after the drain, want every one", len(acked))
	}
}

// TestScanAcksRejectsBadLines pins that the observer trusts nothing:
// malformed lines, a hold marker disagreeing with the schedule, acks
// off the predicted stream, acks beyond the schedule, and streams that
// end without reaching the kill point are all errors.
func TestScanAcksRejectsBadLines(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		in   string
		want string
	}{
		{"malformed ack line", scanSpec(3), "A 0 1\nB 0 2\n", "malformed"},
		{"hold marker off the schedule", scanSpec(0), "A 0 1\nW 5\n", "hold marker"},
		{"hold marker not a number", scanSpec(0), "W x\n", "hold marker"},
		{"ack off the predicted stream", scanSpec(3), "A 0 1\nA 0 2\n", "diverged"},
		{"ack beyond the schedule", scanSpec(0), happyStream(scanSpec(0)) + "A 0 7\n", "diverged"},
		{"stream ends before the kill point", scanSpec(4), "A 0 1\nA 1 1\n", "kill point"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scanAcks(strings.NewReader(tc.in), tc.spec, func() {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("scanAcks = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// TestWaitWriterChildRejectsOtherExits pins the reap check: a child
// that exits cleanly, one that dies of a different signal, and a kill
// call that itself failed are all rejected — only death by the
// injected SIGKILL counts.
func TestWaitWriterChildRejectsOtherExits(t *testing.T) {
	clean := exec.Command("true")
	if err := clean.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	if err := waitWriterChild(clean, nil, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "cleanly") {
		t.Fatalf("a cleanly exited child passed the reap check: %v", err)
	}

	termed := exec.Command("sh", "-c", "kill -TERM $$")
	if err := termed.Start(); err != nil {
		t.Fatalf("start self-terminating child: %v", err)
	}
	if err := waitWriterChild(termed, nil, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "SIGKILL") {
		t.Fatalf("a SIGTERM death passed the reap check: %v", err)
	}

	killed := exec.Command("sh", "-c", "kill -9 $$")
	if err := killed.Start(); err != nil {
		t.Fatalf("start self-killing child: %v", err)
	}
	if err := waitWriterChild(killed, nil, new(bytes.Buffer)); err != nil {
		t.Fatalf("a SIGKILL death failed the reap check: %v", err)
	}

	killedAgain := exec.Command("sh", "-c", "kill -9 $$")
	if err := killedAgain.Start(); err != nil {
		t.Fatalf("start self-killing child: %v", err)
	}
	if err := waitWriterChild(killedAgain, errors.New("kill failed"), new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "kill failed") {
		t.Fatalf("a failed kill call passed the reap check: %v", err)
	}
}

// TestWriterSpecFromEnvRejectsBadValues pins the environment decode:
// every field that does not parse is an error naming the variable.
func TestWriterSpecFromEnvRejectsBadValues(t *testing.T) {
	good := map[string]string{
		writerChildDirEnv:      "/tmp/rig-env-decode",
		writerChildSessionsEnv: "4",
		writerChildEventsEnv:   "16",
		writerChildSeedEnv:     "9",
		writerChildPayloadEnv:  "128",
	}
	cases := []struct {
		name string
		vars map[string]string
		want string
	}{
		{"bad sessions", override(good, writerChildSessionsEnv, "x"), writerChildSessionsEnv},
		{"bad events", override(good, writerChildEventsEnv, "x"), writerChildEventsEnv},
		{"bad seed", override(good, writerChildSeedEnv, "x"), writerChildSeedEnv},
		{"bad payload size", override(good, writerChildPayloadEnv, "x"), writerChildPayloadEnv},
		{"absent events", override(good, writerChildEventsEnv, ""), writerChildEventsEnv},
		{"invalid combination", override(good, writerChildPayloadEnv, "0"), "payload size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.vars {
				t.Setenv(k, v)
			}
			_, err := writerSpecFromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("writerSpecFromEnv = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// override copies vars with one entry replaced ("" removes it).
func override(vars map[string]string, name, value string) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	out[name] = value
	return out
}

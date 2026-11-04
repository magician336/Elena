package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"elena"
)

// checkpointAfterKill simulates the kill-recovery scenario through the
// public API: the seeded events are acknowledged, a checkpoint lands,
// and the process "dies" (the store closes); ack means durable, so the
// checkpoint and every event before it survive.
func checkpointAfterKill(t *testing.T, dir string) {
	t.Helper()
	seedInto(t, dir)
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := store.CreateCheckpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestRunRestoreLatestPrintsPointInTimeRead(t *testing.T) {
	dir := t.TempDir()
	checkpointAfterKill(t, dir)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"restore", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"checkpoint 1 (created ",
		") restored\n",
		"point-in-time read at the baseline:\n",
		"trajectory alpha/runner to seq 2\n",
		"trajectory alpha/scratch to seq 1\n",
		"trajectory beta/empty to seq 0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output missing %q;\ngot:\n%s", want, out)
		}
	}
	for _, want := range []string{`"first"`, `"second"`, `"note"`} {
		if !strings.Contains(out, want) {
			t.Errorf("restore point-in-time read missing payload %s;\ngot:\n%s", want, out)
		}
	}
	i1 := strings.Index(out, "event #1")
	i2 := strings.Index(out, "event #2")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("runner events out of order:\n%s", out)
	}
	if strings.Contains(out, "trajectory beta/empty to seq 0\n  event") {
		t.Errorf("empty trajectory printed event lines:\n%s", out)
	}
	if !strings.Contains(out, "restore read stops at each baseline; events past it stay on disk") {
		t.Errorf("restore output missing the boundary line:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunRestoreSpecificCheckpointStopsAtRecordedSeq(t *testing.T) {
	dir := t.TempDir()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	runner, err := store.CreateSession("alpha", "runner")
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if _, err := runner.Append([]byte("first")); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if _, err := store.CreateCheckpoint(); err != nil { // checkpoint 1, runner at seq 1
		t.Fatalf("checkpoint 1: %v", err)
	}
	if _, err := runner.Append([]byte("second")); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if _, err := store.CreateCheckpoint(); err != nil { // checkpoint 2, runner at seq 2
		t.Fatalf("checkpoint 2: %v", err)
	}
	if _, err := runner.Append([]byte("third")); err != nil {
		t.Fatalf("append third: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"restore", dir, "1"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "checkpoint 1 (created ") {
		t.Errorf("restore did not name checkpoint 1:\n%s", out)
	}
	// The read stops at the sequence number the manifest recorded — the
	// baseline's own timeline — even though events past it sit on disk.
	if !strings.Contains(out, "trajectory alpha/runner to seq 1\n") {
		t.Errorf("restore read did not stop at the recorded seq 1:\n%s", out)
	}
	if !strings.Contains(out, `"first"`) {
		t.Errorf("restore read missing the baseline event:\n%s", out)
	}
	for _, past := range []string{`"second"`, `"third"`} {
		if strings.Contains(out, past) {
			t.Errorf("restore read leaked the post-baseline event %s:\n%s", past, out)
		}
	}
	if !strings.Contains(out, "restore read stops at each baseline; events past it stay on disk") {
		t.Errorf("restore output missing the boundary line:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunRestoreSkipsElapsedSessionsOfTheManifest(t *testing.T) {
	dir := t.TempDir()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	keeper, err := store.CreateSession("alpha", "keeper")
	if err != nil {
		t.Fatalf("create keeper: %v", err)
	}
	if _, err := keeper.Append([]byte("kept")); err != nil {
		t.Fatalf("append kept: %v", err)
	}
	ttl := 10 * time.Millisecond
	shorty, err := store.CreateSessionWithTTL("alpha", "shorty", ttl)
	if err != nil {
		t.Fatalf("create shorty: %v", err)
	}
	if _, err := shorty.Append([]byte("fleeting")); err != nil {
		t.Fatalf("append fleeting: %v", err)
	}
	if _, err := store.CreateCheckpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(ttl + 600*time.Millisecond) // the same elapse margin the root expiry tests use

	var stdout, stderr bytes.Buffer
	code := Run([]string{"restore", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "trajectory alpha/keeper to seq 1\n") {
		t.Errorf("restore read missing the live session of the manifest:\n%s", out)
	}
	if strings.Contains(out, "shorty") || strings.Contains(out, "fleeting") {
		t.Errorf("restore read showed an elapsed session, which reads as if never written:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunRestoreFailures(t *testing.T) {
	empty := t.TempDir()
	killed := t.TempDir()
	checkpointAfterKill(t, killed)

	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"restore", empty}, "checkpoints: none stored"},
		{[]string{"restore", killed, "99"}, "checkpoint not found"},
	} {
		var stdout, stderr bytes.Buffer

		code := Run(tc.args, &stdout, &stderr)

		if code != 1 {
			t.Fatalf("Run(%v) exit code = %d, want 1", tc.args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%v) stdout = %q, want empty", tc.args, stdout.String())
		}
		if !strings.Contains(stderr.String(), tc.wantErr) {
			t.Errorf("Run(%v) stderr = %q, want %q", tc.args, stderr.String(), tc.wantErr)
		}
	}
}

func TestRunRestoreArgCountIsUsageError(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"restore"}, "command restore wants 1 or 2 arguments: <dir> [id]"},
		{[]string{"restore", "a", "b", "c"}, "command restore wants 1 or 2 arguments: <dir> [id]"},
		{[]string{"restore", "a", "xyz"}, `checkpoint id "xyz" is not a number`},
	} {
		var stdout, stderr bytes.Buffer

		code := Run(tc.args, &stdout, &stderr)

		if code != 2 {
			t.Fatalf("Run(%v) exit code = %d, want 2", tc.args, code)
		}
		if !strings.Contains(stderr.String(), tc.wantErr) {
			t.Errorf("Run(%v) stderr = %q, want %q", tc.args, stderr.String(), tc.wantErr)
		}
	}
}

// TestRunRestoreUsageErrorCreatesNoStore pins that usage validation runs
// before the store is opened: a bad checkpoint id must fail without
// leaving a created directory behind (the test's working directory used
// to sprout a store on every run of the table above).
func TestRunRestoreUsageErrorCreatesNoStore(t *testing.T) {
	dir := "usage-must-not-create-this-store"
	var stdout, stderr bytes.Buffer

	code := Run([]string{"restore", dir, "not-a-number"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run exit code = %d, want 2", code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("usage error created %q (stat err = %v); validation must precede the store open", dir, err)
	}
}

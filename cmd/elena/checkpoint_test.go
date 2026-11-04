package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCheckpointCreatesAndReportsID(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"checkpoint", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "checkpoint 1 created at ") {
		t.Errorf("create output = %q, want the checkpoint-1 line first", out)
	}
	if !strings.HasSuffix(out, ", baseline of 3 live sessions\n") {
		t.Errorf("create output = %q, want the live-session count", out)
	}
	assertSignalLines(t, out)
}

func TestRunCheckpointListShowsCreationOrder(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	// Two checkpoints through the CLI itself: the first by command, the
	// second by the library, so the listing mixes both sources.
	if code := Run([]string{"checkpoint", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("first create exit code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"checkpoint", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("second create exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "checkpoint 2 created at ") {
		t.Errorf("second create output = %q, want checkpoint 2", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"checkpoint", "list", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("list exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "checkpoints stored: 2\n") {
		t.Errorf("list output = %q, want the stored count first", out)
	}
	i1 := strings.Index(out, "checkpoint 1 created ")
	i2 := strings.Index(out, "checkpoint 2 created ")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("list not in creation order:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunCheckpointListOfEmptyStore(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := Run([]string{"checkpoint", "list", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("list exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	if stdout.String() != "checkpoints stored: 0\n" {
		t.Errorf("list output = %q, want the zero count alone", stdout.String())
	}
}

func TestRunCheckpointArgCountIsUsageError(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"checkpoint"}, "command checkpoint wants exactly 1 argument: <dir>; to list, use: checkpoint list <dir>"},
		{[]string{"checkpoint", "a", "b"}, "command checkpoint wants exactly 1 argument: <dir>; to list, use: checkpoint list <dir>"},
		{[]string{"checkpoint", "list"}, "command checkpoint list wants exactly 1 argument: <dir>"},
		{[]string{"checkpoint", "list", "a", "b"}, "command checkpoint list wants exactly 1 argument: <dir>"},
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

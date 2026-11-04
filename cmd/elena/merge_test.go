package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"elena"
)

// overwriteOneEvent leaves one dead event on disk through the public
// API: checkpoint, append past the baseline, restore the baseline, then
// append again — the sequence number is occupied, so the append
// overwrites and the stored event becomes dead bytes a merge reclaims.
func overwriteOneEvent(t *testing.T, dir string) {
	t.Helper()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	runner, err := store.CreateSession("alpha", "runner")
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if _, err := runner.Append([]byte("seed")); err != nil {
		t.Fatalf("append seed: %v", err)
	}
	cp, err := store.CreateCheckpoint()
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := runner.Append([]byte("past the baseline")); err != nil {
		t.Fatalf("append past: %v", err)
	}
	if err := store.Restore(cp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := runner.Append([]byte("overwriting")); err != nil {
		t.Fatalf("append overwrite: %v", err)
	}
}

// deadBytesOf extracts the dead-bytes figure from one stats line.
func deadBytesOf(t *testing.T, line string) int64 {
	t.Helper()
	rest := strings.SplitN(line, "dead bytes ", 2)[1]
	rest = strings.SplitN(rest, " ", 2)[0]
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		t.Fatalf("parse dead bytes of %q: %v", line, err)
	}
	return n
}

func TestRunMergeReclaimsDeadBytes(t *testing.T) {
	dir := t.TempDir()
	overwriteOneEvent(t, dir)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"merge", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("merge prints %d lines, want 3 (before, after, reclaimed):\n%s", len(lines), out)
	}
	before, after, reclaimed := lines[0], lines[1], lines[2]
	if !strings.HasPrefix(before, "merge stats before: ") || !strings.HasPrefix(after, "merge stats after:  ") {
		t.Fatalf("merge stats lines missing their prefix:\n%s", out)
	}
	if dead := deadBytesOf(t, before); dead <= 0 {
		t.Errorf("merge before shows %d dead bytes, want > 0 (the overwritten event):\n%s", dead, out)
	}
	if dead := deadBytesOf(t, after); dead != 0 {
		t.Errorf("merge after shows %d dead bytes, want 0:\n%s", dead, out)
	}
	want := int64(0) + deadBytesOf(t, before) - deadBytesOf(t, after)
	if !strings.Contains(reclaimed, strconv.FormatInt(want, 10)+" dead bytes") {
		t.Errorf("reclaimed line %q does not report %d dead bytes", reclaimed, want)
	}
	assertSignalLines(t, out)
}

func TestRunMergeOfEmptyStoreIsNoop(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := Run([]string{"merge", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "merge reclaimed 0 dead bytes\n") {
		t.Errorf("merge of an empty store should report zero reclaimed bytes:\n%s", out)
	}
	if strings.Contains(out, strconv.FormatInt(-1, 10)) {
		t.Errorf("merge reported a negative figure:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunMergeArgCountIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"merge"}, {"merge", "a", "b"}} {
		var stdout, stderr bytes.Buffer

		code := Run(args, &stdout, &stderr)

		if code != 2 {
			t.Fatalf("Run(%v) exit code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "command merge wants exactly 1 argument: <dir>") {
			t.Errorf("Run(%v) stderr = %q, want the usage message", args, stderr.String())
		}
	}
}

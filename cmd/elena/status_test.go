package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunStatusPrintsOverview(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"status", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"store " + dir + ": 2 namespaces, 3 sessions, 3 trajectories",
		"merge view: dead ratio 0.0%, write amplification ",
		", data bytes ",
		", files ",
		"namespace alpha",
		"session runner: perpetual, events 2",
		"session scratch: expires ",
		"namespace beta",
		"session empty: perpetual, events 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q;\ngot:\n%s", want, out)
		}
	}
	if !strings.Contains(out, ", events 1\n") {
		t.Errorf("status output missing the scratch event count;\ngot:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunStatusOfEmptyStore(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := Run([]string{"status", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"store " + dir + ": 0 namespaces, 0 sessions, 0 trajectories",
		"namespace",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q;\ngot:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "namespace "); n != 0 {
		t.Errorf("status of empty store lists %d namespaces, want 0:\n%s", n, out)
	}
	assertSignalLines(t, out)
}

func TestRunStatusArgCountIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", "a", "b"}} {
		var stdout, stderr bytes.Buffer

		code := Run(args, &stdout, &stderr)

		if code != 2 {
			t.Fatalf("Run(%v) exit code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "command status wants exactly 1 argument: <dir>") {
			t.Errorf("Run(%v) stderr = %q, want the usage message", args, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%v) stdout = %q, want empty", args, stdout.String())
		}
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runForTest invokes the CLI Run with a fresh store directory inside
// one temp directory, mirroring how a caller names one run's store.
func runForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	base := t.TempDir()
	full := append([]string{}, args...)
	full = append(full, "--dir", filepath.Join(base, "store"))
	var stdout, stderr bytes.Buffer
	code := Run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestFocusedRunMergeCurveSmoke drives the merge-curve scenario end to
// end on smoke scale: the merge figure line, one row per window, the
// raw JSON report on disk.
func TestFocusedRunMergeCurveSmoke(t *testing.T) {
	jsonOut := filepath.Join(t.TempDir(), "merge-curve.json")
	code, stdout, stderr := runForTest(t,
		"--scenario", "merge-curve",
		"--sessions", "2", "--value-size", "64",
		"--warmup-ops", "50", "--window", "100ms", "--context-windows", "1", "--tail-windows", "1",
		"--json-out", jsonOut,
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "merge-curve: merge wall") {
		t.Fatalf("stdout missing the merge figure line:\n%s", stdout)
	}
	assertJSONReport(t, jsonOut, "merge_elapsed_ns")
}

// TestFocusedRunStartupPairSmoke drives the paired startup scenario
// end to end on smoke scale: both modes' lines over the same dataset
// facts.
func TestFocusedRunStartupPairSmoke(t *testing.T) {
	code, stdout, stderr := runForTest(t,
		"--scenario", "startup-pair",
		"--sessions", "2", "--events", "2", "--value-size", "64",
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "startup hint:") || !strings.Contains(stdout, "startup no-hint:") {
		t.Fatalf("stdout missing the startup pair lines:\n%s", stdout)
	}
}

// TestFocusedRunIndexMemorySmoke drives the per-entry index memory
// scenario end to end on smoke scale, the vendor-reported reference
// printed beside the measured figure.
func TestFocusedRunIndexMemorySmoke(t *testing.T) {
	code, stdout, stderr := runForTest(t,
		"--scenario", "index-memory",
		"--sessions", "2", "--events", "2", "--value-size", "64",
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "index-memory: ") || !strings.Contains(stdout, "vendor-reported") {
		t.Fatalf("stdout missing the index-memory lines:\n%s", stdout)
	}
}

// TestFocusedRunWriteAmpSmoke drives the write-amplification scenario
// end to end on smoke scale: the byte pair before and after one merge
// with the accounting declaration.
func TestFocusedRunWriteAmpSmoke(t *testing.T) {
	code, stdout, stderr := runForTest(t,
		"--scenario", "write-amp",
		"--sessions", "2", "--events", "2", "--value-size", "64",
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "write-amp before merge:") || !strings.Contains(stdout, "write-amp after merge:") {
		t.Fatalf("stdout missing the write-amp lines:\n%s", stdout)
	}
}

// TestFocusedRunUsageErrors pins the argument contract: an unknown
// scenario and an existing directory are wrong asks, exit code 2.
func TestFocusedRunUsageErrors(t *testing.T) {
	code, _, stderr := runForTest(t, "--scenario", "merge")
	if code != 2 || !strings.Contains(stderr, "unknown scenario") {
		t.Fatalf("unknown scenario: exit = %d, stderr = %q", code, stderr)
	}
	existing := t.TempDir()
	var out, errBuf bytes.Buffer
	code = Run([]string{"--scenario", "write-amp", "--dir", existing}, &out, &errBuf)
	if code != 2 || !strings.Contains(errBuf.String(), "exists") {
		t.Fatalf("existing dir: exit = %d, stderr = %q", code, errBuf.String())
	}
}

// assertJSONReport checks the raw JSON report exists and carries the
// declared field.
func assertJSONReport(t *testing.T, path, field string) {
	t.Helper()
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read json report: %v", err)
	}
	if !strings.Contains(string(doc), field) {
		t.Fatalf("json report %s missing field %q", path, field)
	}
}

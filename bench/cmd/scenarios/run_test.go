package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runForTest invokes the CLI Run with a fresh store directory and an
// optional JSON path inside one temp directory, mirroring how a caller
// names one run's artifacts.
func runForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	base := t.TempDir()
	full := append([]string{}, args...)
	full = append(full, "--dir", filepath.Join(base, "store"))
	var stdout, stderr bytes.Buffer
	code := Run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestScenariosRunAppendSmoke drives the pure-append scenario end to
// end on smoke scale: phase lines out, raw JSON report on disk.
func TestScenariosRunAppendSmoke(t *testing.T) {
	jsonOut := filepath.Join(t.TempDir(), "append.json")
	code, stdout, stderr := runForTest(t,
		"--scenario", "append",
		"--sessions", "2", "--events", "3", "--value-size", "64",
		"--json-out", jsonOut,
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "elena append-only: 6 ops") {
		t.Fatalf("stdout missing the append phase line:\n%s", stdout)
	}
	assertJSONReport(t, jsonOut, "elena-append-only")
}

// TestScenariosRunPointSmoke drives the append plus point-in-time-read
// scenario end to end on smoke scale.
func TestScenariosRunPointSmoke(t *testing.T) {
	code, stdout, stderr := runForTest(t,
		"--scenario", "point",
		"--sessions", "2", "--events", "3", "--reads", "2", "--value-size", "64", "--seed", "9",
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "elena point-in-time") {
		t.Fatalf("stdout missing the point phase line:\n%s", stdout)
	}
}

// TestScenariosRunTTLSmoke drives the append plus TTL expiry scenario
// end to end on smoke scale: one generation, a short lifetime, a fast
// sweep, the reclamation chain exercised in seconds.
func TestScenariosRunTTLSmoke(t *testing.T) {
	code, stdout, stderr := runForTest(t,
		"--scenario", "ttl",
		"--sessions", "1", "--events", "2", "--value-size", "64",
		"--generations", "1", "--ttl", "1s", "--sweep-period", "100ms",
	)
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "ttl-append-g0") || !strings.Contains(stdout, "ttl-protected-read") {
		t.Fatalf("stdout missing the TTL phase lines:\n%s", stdout)
	}
}

// TestScenariosRunUsageErrors pins the argument contract: a missing
// scenario and an existing directory are wrong asks, exit code 2.
func TestScenariosRunUsageErrors(t *testing.T) {
	code, _, stderr := runForTest(t, "--sessions", "2")
	if code != 2 || !strings.Contains(stderr, "--scenario is required") {
		t.Fatalf("missing scenario: exit = %d, stderr = %q", code, stderr)
	}
	existing := t.TempDir()
	var out, errBuf bytes.Buffer
	code = Run([]string{"--scenario", "append", "--dir", existing}, &out, &errBuf)
	if code != 2 || !strings.Contains(errBuf.String(), "exists") {
		t.Fatalf("existing dir: exit = %d, stderr = %q", code, errBuf.String())
	}
}

// assertJSONReport checks the raw JSON report exists and names the
// workload it measured.
func assertJSONReport(t *testing.T, path, workload string) {
	t.Helper()
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read json report: %v", err)
	}
	if !strings.Contains(string(doc), workload) {
		t.Fatalf("json report %s missing workload %q", path, workload)
	}
}

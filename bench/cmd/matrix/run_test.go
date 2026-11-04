package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runForTest invokes the CLI Run with a fresh run root and output paths
// inside one temp directory, mirroring how a caller names one run's
// files.
func runForTest(t *testing.T, args []string) (int, string, string, string) {
	t.Helper()
	base := t.TempDir()
	full := append([]string{}, args...)
	full = append(full, "--root", filepath.Join(base, "run"))
	var stdout, stderr bytes.Buffer
	code := Run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String(), base
}

// smokeArgs is the seconds-scale smoke invocation: every driver, one
// mix, one value size, a handful of operations.
func smokeArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	args := []string{
		"--engines", "elena,bbolt,badger,rosedb",
		"--mixes", "a",
		"--values", "64",
		"--keys", "4",
		"--run-ops", "4",
		"--seed", "9",
		"--sync-period", "5ms",
	}
	return append(args, extra...)
}

// TestMatrixRunEndToEndSmoke drives the whole matrix CLI end to end on
// smoke scale: every engine, one mix, one value size, the raw JSON and
// markdown reports on disk — the same shape a full run takes, seconds
// instead of hours.
func TestMatrixRunEndToEndSmoke(t *testing.T) {
	base := t.TempDir()
	jsonOut := filepath.Join(base, "matrix.json")
	mdOut := filepath.Join(base, "matrix.md")
	code, stdout, stderr, _ := runForTest(t, smokeArgs(t,
		"--json-out", jsonOut,
		"--md-out", mdOut,
	))
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}

	for _, driver := range []string{"elena", "bbolt", "badger", "rosedb"} {
		if !strings.Contains(stdout, "cell "+driver+"-per-write-sync-ycsb-a-64B") {
			t.Fatalf("stdout missing %s's cell line:\n%s", driver, stdout)
		}
	}
	if !strings.Contains(stdout, "cell elena-group-commit-ycsb-a-64B") {
		t.Fatalf("stdout missing elena's group-commit cell line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "matrix: 5 cells produced, 0 failures") {
		t.Fatalf("stdout missing the summary line:\n%s", stdout)
	}

	jsonDoc, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("read json report: %v", err)
	}
	for _, want := range []string{`"declaration"`, `"elena_mapping"`, `"sync"`, `"results"`} {
		if !strings.Contains(string(jsonDoc), want) {
			t.Fatalf("json report missing %q:\n%s", want, jsonDoc)
		}
	}
	mdDoc, err := os.ReadFile(mdOut)
	if err != nil {
		t.Fatalf("read markdown report: %v", err)
	}
	if !strings.Contains(string(mdDoc), "# YCSB matrix report") || !strings.Contains(string(mdDoc), "durability, engine by engine:") {
		t.Fatalf("markdown report missing its declaration sections:\n%s", mdDoc)
	}
}

// TestMatrixRunSubset pins the subset contract: one engine, one mix,
// one value size runs exactly that slice — two cells for elena's two
// tiers, and no line of any other engine.
func TestMatrixRunSubset(t *testing.T) {
	code, stdout, stderr, _ := runForTest(t, []string{
		"--engines", "elena",
		"--mixes", "b",
		"--values", "256",
		"--keys", "4",
		"--run-ops", "4",
		"--sync-period", "5ms",
	})
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "cell elena-per-write-sync-ycsb-b-256B") ||
		!strings.Contains(stdout, "cell elena-group-commit-ycsb-b-256B") {
		t.Fatalf("stdout missing elena's two cells:\n%s", stdout)
	}
	for _, other := range []string{"bbolt", "badger", "rosedb"} {
		if strings.Contains(stdout, other) {
			t.Fatalf("stdout mentions filtered-out engine %s:\n%s", other, stdout)
		}
	}
}

// TestMatrixRunUsageErrors pins the wrong-ask exits: a missing root,
// unknown engines, mixes, and distributions, malformed value lists and
// periods, and flag syntax problems all print the usage surface and
// exit 2, never touching a store.
func TestMatrixRunUsageErrors(t *testing.T) {
	base := []string{"--engines", "elena", "--keys", "4", "--run-ops", "4"}
	tests := []struct {
		name string
		args []string
	}{
		{name: "no root", args: base},
		{name: "unknown engine", args: append(append([]string{}, base...), "--engines", "pebble", "--root", "x")},
		{name: "empty engine list", args: append(append([]string{}, base...), "--engines", "", "--root", "x")},
		{name: "unknown mix", args: append(append([]string{}, base...), "--mixes", "z", "--root", "x")},
		{name: "empty mix list", args: append(append([]string{}, base...), "--mixes", "", "--root", "x")},
		{name: "empty value list", args: append(append([]string{}, base...), "--values", "", "--root", "x")},
		{name: "malformed value", args: append(append([]string{}, base...), "--values", "64,x", "--root", "x")},
		{name: "zero value", args: append(append([]string{}, base...), "--values", "0", "--root", "x")},
		{name: "zero keys", args: append(append([]string{}, base...), "--keys", "0", "--root", "x")},
		{name: "zero run ops", args: append(append([]string{}, base...), "--run-ops", "0", "--root", "x")},
		{name: "zero seed", args: append(append([]string{}, base...), "--seed", "0", "--root", "x")},
		{name: "malformed seed", args: append(append([]string{}, base...), "--seed", "x", "--root", "x")},
		{name: "malformed period", args: append(append([]string{}, base...), "--sync-period", "fast", "--root", "x")},
		{name: "zero period", args: append(append([]string{}, base...), "--sync-period", "0s", "--root", "x")},
		{name: "unknown dist", args: append(append([]string{}, base...), "--dist", "gaussian", "--root", "x")},
		{name: "negative theta", args: append(append([]string{}, base...), "--dist", "zipf", "--zipf-theta", "0", "--root", "x")},
		{name: "flag syntax", args: append(append([]string{}, base...), "--root")},
		{name: "unexpected arguments", args: append(append([]string{}, base...), "--root", "x", "extra")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("Run exit = %d, want 2 (stderr: %s)", code, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("usage error printed nothing to stderr")
			}
		})
	}
}

// TestMatrixRunDefaultEngines pins the default engine list: --engines
// omitted means every registered engine, on a read-only smoke slice
// that keeps the check cheap.
func TestMatrixRunDefaultEngines(t *testing.T) {
	code, stdout, stderr, _ := runForTest(t, []string{
		"--mixes", "c",
		"--values", "64",
		"--keys", "2",
		"--run-ops", "2",
		"--sync-period", "5ms",
	})
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	for _, engine := range []string{"elena", "bbolt", "badger", "rosedb"} {
		if !strings.Contains(stdout, "cell "+engine+"-per-write-sync-ycsb-c-64B") {
			t.Fatalf("stdout missing default engine %s:\n%s", engine, stdout)
		}
	}
}

// TestMatrixRunExistingRootFails pins the fresh-root contract: an
// existing --root refuses the run — a rerun over leftovers is not a
// matrix — and exits 1 as a run failure, not a usage problem.
func TestMatrixRunExistingRootFails(t *testing.T) {
	base := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(append(smokeArgs(t), "--root", base), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
}

// TestMatrixRunExistingOutputPathFails pins the report contract: an
// existing --json-out refuses to be overwritten — a report file is one
// run's artifact, not a rolling log.
func TestMatrixRunExistingOutputPathFails(t *testing.T) {
	base := t.TempDir()
	jsonOut := filepath.Join(base, "matrix.json")
	if err := os.WriteFile(jsonOut, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stale report: %v", err)
	}
	code, _, stderr, _ := runForTest(t, smokeArgs(t, "--json-out", jsonOut))
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1 (stderr: %s)", code, stderr)
	}
}

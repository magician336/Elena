package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// runForTest invokes the CLI Run with a fresh subdirectory of a temp
// dir, mirroring how every caller names one run's directory.
func runForTest(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	full := append([]string{}, args...)
	// append the dir flag to the last non-dir-flag invocation
	if len(full) == 0 {
		t.Fatal("runForTest wants arguments")
	}
	full = append(full, "--dir", filepath.Join(dir, "run"))
	var stdout, stderr bytes.Buffer
	code := Run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestRunMicroWorkloadPerDriver drives each driver end to end through
// the CLI entry: a fresh directory, a micro workload, and the
// throughput and latency numbers of both phases on stdout.
func TestRunMicroWorkloadPerDriver(t *testing.T) {
	for _, driver := range []string{"elena", "bbolt", "badger", "rosedb"} {
		t.Run(driver, func(t *testing.T) {
			code, stdout, stderr := runForTest(t, []string{
				"--driver", driver, "--ops", "5", "--value-size", "16",
			})
			if code != 0 {
				t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
			}
			for _, want := range []string{
				"driver " + driver,
				"phase append: 5 ops",
				"phase read: 5 ops",
				"ops/sec",
			} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout missing %q;\nstdout: %s", want, stdout)
				}
			}
		})
	}
}

// TestRunUsageErrors pins the wrong-ask exits: a missing or unknown
// driver, non-positive operation counts and value sizes, and flag
// syntax problems all print the usage surface and exit 2, never
// touching a store.
func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no driver", args: []string{"--ops", "5"}},
		{name: "empty driver", args: []string{"--driver", ""}},
		{name: "unknown driver", args: []string{"--driver", "rosedb"}},
		{name: "zero ops", args: []string{"--driver", "elena", "--ops", "0"}},
		{name: "negative ops", args: []string{"--driver", "elena", "--ops", "-1"}},
		{name: "zero value size", args: []string{"--driver", "bbolt", "--value-size", "0"}},
		{name: "no dir", args: []string{"--driver", "elena", "--ops", "5"}},
		{name: "flag syntax", args: []string{"--driver", "elena", "--ops", "abc"}},
		{name: "no arguments", args: []string{}},
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

// TestRunExistingDirectoryFails pins the fresh-directory contract: an
// existing --dir refuses the run — a rerun over leftovers is not a
// micro workload — and exits 1 as a run failure, not a usage problem.
func TestRunExistingDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--driver", "elena", "--ops", "5", "--dir", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
}

// TestRunDefaultsApply pins the documented defaults by their effect:
// with --ops and --value-size omitted the run still reports both
// phases with the default operation count.
func TestRunDefaultsApply(t *testing.T) {
	code, stdout, stderr := runForTest(t, []string{"--driver", "bbolt"})
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "phase append: 200 ops") || !strings.Contains(stdout, "phase read: 200 ops") {
		t.Fatalf("stdout missing default op counts:\n%s", stdout)
	}
}

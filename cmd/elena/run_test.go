package main

import (
	"bytes"
	"strings"
	"testing"
)

// signalFirstWords is the narrative-hygiene vocabulary: every non-empty
// line of CLI output must begin with one of these system signals (ADR
// 0001). The agent appears nowhere in CLI copy at all.
var signalFirstWords = map[string]bool{
	"baseline":     true,
	"baselines":    true,
	"command":      true,
	"commands":     true,
	"checkpoint":   true,
	"checkpoints":  true,
	"dump":         true,
	"event":        true,
	"events":       true,
	"help":         true,
	"merge":        true,
	"namespace":    true,
	"namespaces":   true,
	"point-in-time": true,
	"recovery":     true,
	"restore":      true,
	"session":      true,
	"space":        true,
	"stats":        true,
	"status":       true,
	"store":        true,
	"sweep":        true,
	"trajectory":   true,
	"usage":        true,
}

// assertSignalLines checks every non-empty line of output against the
// system-signal vocabulary.
func assertSignalLines(t *testing.T, output string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		first := strings.TrimSuffix(strings.Fields(line)[0], ":")
		if !signalFirstWords[first] {
			t.Errorf("output line %q starts with %q, not a system signal", line, first)
		}
	}
}

func TestRunWithoutArgumentsPrintsUsageToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(nil, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run(nil) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	assertUsage(t, stderr.String())
	assertSignalLines(t, stderr.String())
}

func TestRunHelpPrintsUsageToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var stdout, stderr bytes.Buffer

		code := Run([]string{arg}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("Run(%q) exit code = %d, want 0", arg, code)
		}
		if stderr.Len() != 0 {
			t.Errorf("Run(%q) stderr = %q, want empty", arg, stderr.String())
		}
		assertUsage(t, stdout.String())
		assertSignalLines(t, stdout.String())
	}
}

func TestRunUnknownCommandFailsWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run([]string{"nope"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run(nope) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `command "nope" not found`) {
		t.Errorf("stderr = %q, want the unknown-command line", stderr.String())
	}
	assertUsage(t, stderr.String())
	assertSignalLines(t, stderr.String())
}

// assertUsage pins the shape of the usage text: the usage line, every
// command row, and the sweep/merge boundary lines.
func assertUsage(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{
		"usage: elena <command> [arguments]",
		"status",
		"dump",
		"merge",
		"checkpoint",
		"restore",
		"help",
			"stats: dead ratio reads the directory's bytes; write amplification counts this process's writes",
		"baselines: a restore resets runtime state; events past the baseline stay on disk, the printed read stops at the recorded sequence",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage text missing %q", want)
		}
	}
	if strings.Contains(out, "Agent") {
		t.Errorf("usage text mentions the agent outside a scenario line")
	}
}

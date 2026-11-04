package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Narration is the demo's product, so the test pins its lines: the
// session, every handed-out sequence number, the full read's four
// events, and the point-in-time read's two, endpoint included. Append
// timestamps follow the clock; every other printed fact is
// deterministic.
func TestRunNarratesAppendsFullReadAndPointInTimeRead(t *testing.T) {
	out := narrate(t)

	for _, want := range []string{
		"store directory: ",
		`session created: namespace "runs", session "alpha", lifecycle perpetual`,
		`event appended: seq 1, payload "session opened"`,
		`event appended: seq 2, payload "tool result: 3 matches"`,
		`event appended: seq 3, payload "plan revised: 2 steps"`,
		`event appended: seq 4, payload "answer drafted"`,
		"trajectory full read: 4 events",
		"  seq 1 appended-at 20",
		"  seq 4 appended-at 20",
		"point-in-time read to seq 2: 2 events",
		`  seq 2 payload "tool result: 3 matches"`,
		"store closed: acknowledged events are durable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narration lacks %q;\ngot:\n%s", want, out)
		}
	}

	full := strings.Index(out, "trajectory full read")
	pitr := strings.Index(out, "point-in-time read to seq 2")
	if full < 0 || pitr < 0 || full > pitr {
		t.Errorf("narration order: full read at %d, point-in-time read at %d, want the full read first", full, pitr)
	}
}

// Narration lines open with a system signal — a store object or
// operation — never with a person or a pitch (ADR-0001).
func TestNarrationOpensWithSystemSignals(t *testing.T) {
	allowed := []string{
		"store", "session", "event", "trajectory", "point-in-time", "seq",
	}
	for _, line := range strings.Split(narrate(t), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		first := strings.ToLower(strings.TrimSuffix(fields[0], ":"))
		if !slices.Contains(allowed, first) {
			t.Errorf("narration line %q opens with %q, want a system signal from %v", line, first, allowed)
		}
	}
}

// Narration is public copy: career-adjacent vocabulary and first-person
// voice never appear (ADR-0001 public-face rule).
func TestNarrationStaysOnSystemVocabulary(t *testing.T) {
	lower := strings.ToLower(narrate(t))
	for _, banned := range []string{"learn", "tutorial", "interview", "resume", "career", "hiring", "job", "we ", "you", "our "} {
		if strings.Contains(lower, banned) {
			t.Errorf("narration contains %q — public copy stays on system vocabulary", banned)
		}
	}
}

// Runnability is the acceptance fact: go run . must start, narrate, and
// exit zero. -dir points the demo at a temporary directory so the
// smoke run leaves nothing behind.
func TestGoRunSmoke(t *testing.T) {
	if v := os.Getenv(skipGoRunEnv); v != "" {
		t.Skipf("go run smoke disabled via %s=%q", skipGoRunEnv, v)
	}
	goBin := goBinary()
	if _, err := os.Stat(goBin); err != nil {
		t.Skipf("go binary %q not found; set GO to the toolchain to smoke-test go run", goBin)
	}
	dir := filepath.Join(t.TempDir(), "store")
	cmd := exec.Command(goBin, "run", ".", "-dir", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run . -dir %q failed: %v\noutput:\n%s", dir, err, out)
	}
	if !strings.Contains(string(out), "trajectory full read: 4 events") {
		t.Errorf("go run output lacks the full-read line:\n%s", out)
	}
}

// narrate runs the demo against a fresh temporary directory and returns
// what it printed.
func narrate(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	var buf bytes.Buffer
	if err := run(&buf, dir); err != nil {
		t.Fatalf("run(%q) failed: %v", dir, err)
	}
	return buf.String()
}

// skipGoRunEnv gates the go-run smoke test where spawning the toolchain
// is unwelcome; unset by default, like the engine's kill -9 harness
// switch.
const skipGoRunEnv = "ELENA_EXAMPLES_SKIP_GO_RUN"

// goBinary locates the toolchain for the smoke test: the GO override
// make uses, then PATH, then the standard /usr/local/go install root
// (runtime.GOROOT is deprecated for exactly this lookup).
func goBinary() string {
	if v := os.Getenv("GO"); v != "" {
		return v
	}
	if v, err := exec.LookPath("go"); err == nil {
		return v
	}
	return "/usr/local/go/bin/go"
}

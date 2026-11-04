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
// atomic commit carrying an event group and its baseline checkpoint,
// the reopen that lists the stored checkpoint, the restore that rolls
// the sequence counters back, the resumed append that reuses the
// baseline's next sequence number, and the two reads that show the
// boundary — the point-in-time read stops at the baseline while the
// full read still shows the post-baseline events.
func TestRunNarratesCheckpointRestoreAndPointInTimeContrast(t *testing.T) {
	out := narrate(t)

	for _, want := range []string{
		"store directory: ",
		`event appended: seq 1, payload "task opened"`,
		`event appended: seq 2, payload "tool result: schema loaded"`,
		"batch committed: seqs [3 4 5], checkpoint id 1 — one append, one fdatasync: the event group and its baseline survive as a whole",
		`event appended: seq 6, payload "interim answer stored"`,
		`event appended: seq 7, payload "final answer stored"`,
		"store closed: acknowledged events and the checkpoint are durable — the demo's stand-in for a crash",
		"store reopened: same directory, same lock discipline",
		"checkpoints listed: id 1 (created-at 20",
		"restore applied: checkpoint id 1 — sequence counters back at the baseline's seq 5; a baseline to recover to, not a time machine",
		`event appended: seq 6, payload "resumed after restore" — the baseline's next sequence number, overwriting the pre-restore seq 6; last write wins`,
		"point-in-time read to seq 5: 5 events — the trajectory as of the baseline",
		"trajectory full read: 7 events — data after the baseline is still on disk and still visible",
		`  seq 6 payload "resumed after restore"`,
		`  seq 7 payload "final answer stored"`,
		"store closed: acknowledged events are durable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narration lacks %q;\ngot:\n%s", want, out)
		}
	}

	restore := strings.Index(out, "restore applied")
	resumed := strings.Index(out, `payload "resumed after restore"`)
	pitr := strings.Index(out, "point-in-time read to seq 5")
	full := strings.Index(out, "trajectory full read")
	for _, tc := range []struct {
		name      string
		firstThan int
	}{
		{"restore before the resumed append", resumed},
		{"point-in-time read before the full read", full},
	} {
		if restore < 0 || tc.firstThan < 0 || restore > tc.firstThan {
			t.Errorf("narration order: restore at %d must precede its consequence at %d", restore, tc.firstThan)
		}
	}
	if pitr < 0 || full < 0 || pitr > full {
		t.Errorf("narration order: point-in-time read at %d, full read at %d, want the point-in-time read first", pitr, full)
	}
}

// Narration lines open with a system signal — a store object or
// operation — never with a person or a pitch (ADR-0001).
func TestNarrationOpensWithSystemSignals(t *testing.T) {
	allowed := []string{
		"store", "session", "event", "trajectory", "point-in-time", "seq",
		"batch", "checkpoints", "restore",
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
// voice never appear (ADR-0001 public-face rule). "Resume" the verb
// (resume from a checkpoint) is systems vocabulary and stays allowed;
// the career noun never appears in this codebase's copy.
func TestNarrationStaysOnSystemVocabulary(t *testing.T) {
	lower := strings.ToLower(narrate(t))
	for _, banned := range []string{"learn", "tutorial", "interview", "career", "hiring", "job", "we ", "you", "our "} {
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
	if !strings.Contains(string(out), "trajectory full read: 7 events") {
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

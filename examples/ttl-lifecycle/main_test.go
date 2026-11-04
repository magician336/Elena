package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Demo pacing for the test: a ttl of 400ms and a sweep period of
// 1500ms leave a 1.1s window between the deadline (visibility turns)
// and the first sweep check that can merge — the "expired but not yet
// reclaimed" observation in between is safe, and both waits poll.
const (
	testTTL   = 400 * time.Millisecond
	testSweep = 1500 * time.Millisecond
)

// Narration is the demo's product, so the test pins both halves. The
// visibility half: before the deadline the session and its events read
// normally, after it the whole session reads as if never written —
// events, single reads, listings, and appends alike — while the bytes
// still stand on disk. The space half: the sweep notices the elapsed
// deadline, one merge runs, and the bytes come back.
func TestRunNarratesBothHalvesOfTheTTLLifecycle(t *testing.T) {
	out := narrate(t)

	for _, want := range []string{
		"store directory: ",
		"store opened: sweep period 1.5s (default: one check per minute; see WithSweepPeriod)",
		`session created: namespace "runs", session "scratch", ttl 400ms — one deadline shared by the session's metadata and every event`,
		`event appended: seq 1, payload "scratch value: 42"`,
		`event appended: seq 2, payload "scratch page fetched"`,
		`event appended: seq 3, payload "scratch fact extracted"`,
		"events read before the deadline: 3 events",
		"sessions listed before the deadline: 1 session",
		"stats before the deadline: live entries 4, live bytes ",
		"reads as if never written",
		"events read after the deadline: 0 events",
		"event read after the deadline: seq 1: ErrEventNotFound",
		"append after the deadline: ErrSessionEnded",
		"sessions listed after the deadline: none",
		" — visibility and occupancy are separate facts; reclaiming the bytes falls to the sweep",
		"merge ran: the sweep noticed the elapsed deadline and triggered one merge — expired entries omitted, bytes and index memory returned",
		"stats after the merge: live entries 0, live bytes 0, data bytes 0, dead ratio 0.00",
		"events read after the merge: 0 events — visibility never moved",
		"store closed: acknowledged events are durable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narration lacks %q;\ngot:\n%s", want, out)
		}
	}

	// Occupancy contrast: the data-bytes figure after the deadline must
	// equal the figure before it — the session is invisible but still
	// occupies the disk until the merge.
	match := occupancyPattern.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("narration lacks the occupancy line;\ngot:\n%s", out)
	}
	if match[1] != match[2] {
		t.Errorf("occupancy moved without a merge: data bytes %s -> %s, want unchanged", match[1], match[2])
	}

	invisible := strings.Index(out, "reads as if never written")
	merged := strings.Index(out, "merge ran:")
	if invisible < 0 || merged < 0 || invisible > merged {
		t.Errorf("narration order: invisibility at %d, merge at %d, want invisibility first", invisible, merged)
	}
}

var occupancyPattern = regexp.MustCompile(`stats after the deadline: data bytes (\d+), unchanged from (\d+) `)

// Narration lines open with a system signal — a store object or
// operation — never with a person or a pitch (ADR-0001).
func TestNarrationOpensWithSystemSignals(t *testing.T) {
	allowed := []string{
		"store", "session", "event", "events", "seq", "deadline",
		"sessions", "stats", "append", "merge",
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
// exit zero. -ttl and -sweep shrink the pacing so the smoke run stays
// quick; -dir points the demo at a temporary directory so it leaves
// nothing behind.
func TestGoRunSmoke(t *testing.T) {
	if v := os.Getenv(skipGoRunEnv); v != "" {
		t.Skipf("go run smoke disabled via %s=%q", skipGoRunEnv, v)
	}
	goBin := goBinary()
	if _, err := os.Stat(goBin); err != nil {
		t.Skipf("go binary %q not found; set GO to the toolchain to smoke-test go run", goBin)
	}
	dir := filepath.Join(t.TempDir(), "store")
	cmd := exec.Command(goBin, "run", ".",
		"-dir", dir, "-ttl", testTTL.String(), "-sweep", testSweep.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run . -dir %q failed: %v\noutput:\n%s", dir, err, out)
	}
	if !strings.Contains(string(out), "stats after the merge: live entries 0") {
		t.Errorf("go run output lacks the post-merge stats line:\n%s", out)
	}
}

// narrated caches the demo's output across the narration tests: the
// demo paces in wall-clock seconds and its lines are deterministic, so
// the tests share one run instead of paying for three.
var narrated struct {
	once sync.Once
	out  string
}

// narrate runs the demo against a fresh temporary directory and returns
// what it printed; the first failure fails the calling test.
func narrate(t *testing.T) string {
	t.Helper()
	narrated.once.Do(func() {
		dir := filepath.Join(t.TempDir(), "store")
		var buf bytes.Buffer
		if err := run(&buf, dir, testTTL, testSweep); err != nil {
			t.Fatalf("run(%q, %s, %s) failed: %v", dir, testTTL, testSweep, err)
		}
		narrated.out = buf.String()
	})
	return narrated.out
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

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixtures in this file are fixed violation samples: every wording
// rule must fire on its sample, and clean samples written in the
// project's actual vocabulary must stay silent.

func TestNarrativeFilesFlagBannedTerms(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		rule string
	}{
		{"trace", "docs/story.md", "The trace of one session.", "glossary/trajectory"},
		{"traces", "README.md", "Sessions hold traces of events.", "glossary/trajectory"},
		{"traced", "web/index.html", "Events are traced in order.", "glossary/trajectory"},
		{"snapshot", "docs/story.md", "Write a snapshot to disk.", "glossary/checkpoint"},
		{"snapshots", "docs/story.md", "Two snapshots on disk.", "glossary/checkpoint"},
		{"savepoint", "docs/story.md", "Restore from the savepoint.", "glossary/checkpoint"},
		{"compaction", "docs/story.md", "Compaction reclaims dead space.", "glossary/merge"},
		{"compactions", "docs/story.md", "Two compactions ran.", "glossary/merge"},
		{"replay", "docs/story.md", "Replay the events to seq 4.", "glossary/point-in-time"},
		{"replaying", "web/app.js", "Replaying the whole log is slow.", "glossary/point-in-time"},
		{"tenant", "docs/story.md", "One tenant per key range.", "glossary/namespace"},
		{"tenants", "docs/story.md", "Tenants share nothing.", "glossary/namespace"},
		{"multi-tenant", "docs/story.md", "A multi-tenant deployment.", "glossary/namespace"},
		{"prefix", "docs/story.md", "Use a prefix to group keys.", "glossary/namespace-prefix"},
		{"prefixes", "docs/story.md", "Two prefixes collide.", "glossary/namespace-prefix"},
		{"conversation", "docs/story.md", "The conversation continues.", "glossary/session"},
		{"chinese replay word", "docs/story.md", "对轨迹做回放即可恢复视图。", "glossary/point-in-time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkFile(File{Path: tc.path, Content: []byte("clean intro\n" + tc.line + "\n")})
			if len(got) != 1 {
				t.Fatalf("checkFile(%s) violations = %v, want exactly 1", tc.path, got)
			}
			v := got[0]
			if v.Line != 2 {
				t.Errorf("violation line = %d, want 2", v.Line)
			}
			if v.Rule != tc.rule {
				t.Errorf("violation rule = %q, want %q", v.Rule, tc.rule)
			}
			if v.Term == "" {
				t.Error("violation term is empty, want the matched text")
			}
		})
	}
}

func TestNarrativeFilesStayCleanOnProjectVocabulary(t *testing.T) {
	clean := strings.Join([]string{
		"# Elena",
		"",
		"A checkpoint is the restore baseline; the merge reclaims dead records.",
		"Namespaces isolate sessions; a point-in-time read returns events up to a sequence number.",
		"Trajectories are append-only; each event is one immutable record.",
		"",
	}, "\n")
	got := checkFile(File{Path: "README.md", Content: []byte(clean)})
	if len(got) != 0 {
		t.Fatalf("clean narrative fixture violations = %v, want none", got)
	}
}

func TestGoProfilesEngineRelaxedElsewhereStrict(t *testing.T) {
	engineLower := "// The scan reads a snapshot of the index taken under mu."
	strictLower := "// Stats returns a snapshot of the store's observables."

	if got := checkFile(File{Path: "internal/engine/scan.go", Content: []byte(engineLower)}); len(got) != 0 {
		t.Errorf("engine comment with generic lowercase snapshot flagged: %v, want none", got)
	}
	if got := checkFile(File{Path: "stats.go", Content: []byte(strictLower)}); len(got) != 1 {
		t.Errorf("facade comment calling Stats a snapshot violations = %v, want 1", got)
	}
	if got := checkFile(File{Path: "internal/semantic/read.go", Content: []byte(strictLower)}); len(got) != 1 {
		t.Errorf("semantic comment violations = %v, want 1", got)
	}
	if got := checkFile(File{Path: "cmd/elena/merge.go", Content: []byte(strictLower)}); len(got) != 1 {
		t.Errorf("cmd comment violations = %v, want 1", got)
	}
	if got := checkFile(File{Path: "examples/session/main.go", Content: []byte(strictLower)}); len(got) != 1 {
		t.Errorf("examples comment violations = %v, want 1", got)
	}
}

func TestEngineIdentifiersAreFlagged(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		rule string
	}{
		{
			"test name compound",
			"internal/engine/reopen_test.go",
			"func TestCleanReopenReplaysOverwritesAndTombstones(t *testing.T) {}",
			"glossary/point-in-time",
		},
		{
			"field name compound",
			"internal/engine/scan.go",
			"scanSnapshot := idx.clone()",
			"glossary/checkpoint",
		},
		{
			"standalone identifier",
			"internal/engine/kv.go",
			"tenant := decodeLabel(k)",
			"glossary/namespace",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkFile(File{Path: tc.path, Content: []byte(tc.line + "\n")})
			if len(got) != 1 || got[0].Rule != tc.rule {
				t.Fatalf("violations = %v, want exactly 1 with rule %q", got, tc.rule)
			}
		})
	}
}

func TestViolationsAreSorted(t *testing.T) {
	content := "line one has a snapshot\nline two has a trace\n"
	got := checkFile(File{Path: "a.md", Content: []byte(content)})
	rules := []string{got[0].Rule, got[1].Rule}
	want := []string{"glossary/checkpoint", "glossary/trajectory"}
	if !slices.Equal(rules, want) {
		t.Errorf("violation order = %v, want %v", rules, want)
	}
}

func TestGCOnlyAllowedInRuntimeContext(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		want bool
	}{
		{"bare GC", "docs/story.md", "The GC reclaims dead entries.", true},
		{"GC in merge docs", "README.md", "merge triggers GC at a threshold", true},
		{"runtime GC allowed", "docs/benchmarks/report.md", "Pauses from the Go runtime GC are negligible.", false},
		{"runtime.GC call allowed", "internal/engine/engine.go", "_ = runtime.GC()", false},
		{"garbage collector allowed", "docs/benchmarks/report.md", "The garbage collector pauses stay under 1ms.", false},
		{"gc lowercase not matched", "docs/story.md", "The gc lowercased form is not the term.", false},
		{"ENGINEGC not matched", "internal/engine/merge.go", "x := ENGINEGC", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkFile(File{Path: tc.path, Content: []byte(tc.line + "\n")})
			flagged := false
			for _, v := range got {
				if v.Rule == "glossary/merge" && v.Term == "GC" {
					flagged = true
				}
			}
			if flagged != tc.want {
				t.Errorf("GC flagged = %v, want %v (violations: %v)", flagged, tc.want, got)
			}
		})
	}
}

func TestWordBoundariesRejectPartialMatches(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
	}{
		{"traceback", "internal/semantic/read.go", "// A debug traceback of the failure."},
		{"traceable", "cmd/elena/run.go", "// The run is traceable through logs."},
		{"compact adjective", "internal/engine/hint.go", "// Hint files: a compact key-to-position map."},
		{"replayer word boundary", "internal/engine/recover.go", "// replayedEntry is one replayed entry."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkFile(File{Path: tc.path, Content: []byte(tc.line + "\n")}); len(got) != 0 {
				t.Errorf("violations = %v, want none", got)
			}
		})
	}
}

func TestStrictGoFlagsLowerCamelCompounds(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		rule string
	}{
		{"lowerCamel snapshot", "internal/semantic/read.go", "readSnapshot := events[:n]", "glossary/checkpoint"},
		{"replay ident", "elena.go", "eventsReplay, err := sess.Events()", "glossary/point-in-time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkFile(File{Path: tc.path, Content: []byte(tc.line + "\n")})
			if len(got) != 1 || got[0].Rule != tc.rule {
				t.Fatalf("violations = %v, want exactly 1 with rule %q", got, tc.rule)
			}
		})
	}
}

func TestAttributionRules(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		want string
	}{
		{
			"co-authored-by",
			"docs/story.md",
			"Co-Authored-By: someone",
			"attribution/co-author",
		},
		{
			"generated with claude",
			"docs/story.md",
			"Generated with Claude Code",
			"attribution/generated-with",
		},
		{
			"generated by gpt",
			"README.md",
			"This file was generated by ChatGPT yesterday.",
			"attribution/generated-with",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkFile(File{Path: tc.path, Content: []byte(tc.line + "\n")})
			if len(got) != 1 || got[0].Rule != tc.want {
				t.Fatalf("violations = %v, want exactly 1 with rule %q", got, tc.want)
			}
		})
	}
}

func TestLicenseProseStaysClean(t *testing.T) {
	// A sentence shaped like the Apache-2.0 LICENSE text: "generated
	// by" without any tool name must not fire the generated-with rule,
	// and the license's ordinary prose must not trip any term rule.
	license := "within a display generated by the Derivative Works, if and wherever such third-party notices then appear.\n"
	if got := checkFile(File{Path: "LICENSE", Content: []byte(license)}); len(got) != 0 {
		t.Fatalf("LICENSE-shaped prose violations = %v, want none", got)
	}
}

func TestPathsAreScannedAsNamingSurface(t *testing.T) {
	cases := []struct {
		name string
		path string
		want int // violations in the path itself (line 0)
	}{
		{"replay in dir name", "examples/session-write-replay/main.go", 1},
		{"snapshot in file name", "internal/semantic/snapshot_test.go", 1},
		{"prefix not flagged in code path", "internal/engine/prefix.go", 0},
		{"clean path", "internal/semantic/checkpoint.go", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := 0
			for _, v := range checkFile(File{Path: tc.path, Content: []byte("package semantic\n")}) {
				if v.Line == 0 {
					got++
				}
			}
			if got != tc.want {
				t.Errorf("path violations = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExcludedPathsAreSkippedEntirely(t *testing.T) {
	glossary := "_Avoid_: trace、snapshot、compaction、replay、tenant、prefix\n"
	if got := checkFile(File{Path: "GLOSSARY.md", Content: []byte(glossary)}); len(got) != 0 {
		t.Errorf("GLOSSARY.md violations = %v, want none", got)
	}
	self := "prose: regexp.MustCompile(`(?i)\\b(?:snapshots?|replays?)\\b`)\n"
	if got := checkFile(File{Path: "tools/termcheck/rules.go", Content: []byte(self)}); len(got) != 0 {
		t.Errorf("checker self violations = %v, want none", got)
	}
	manifest := "\tgo.opentelemetry.io/otel/trace v1.41.0 // indirect\n"
	if got := checkFile(File{Path: "bench/go.mod", Content: []byte(manifest)}); len(got) != 0 {
		t.Errorf("bench/go.mod violations = %v, want none", got)
	}
}

func TestNonTextFilesGetPathCheckOnly(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}
	png = append(png, []byte("a snapshot of results")...)
	got := checkFile(File{Path: "docs/bench-summary.png", Content: png})
	if len(got) != 0 {
		t.Fatalf("binary content violations = %v, want none (path clean)", got)
	}
	got = checkFile(File{Path: "docs/trace-diagram.png", Content: png})
	if len(got) != 1 || got[0].Line != 0 || got[0].Rule != "glossary/trajectory" {
		t.Fatalf("violations = %v, want one line-0 glossary/trajectory", got)
	}
}

func TestRunReportsViolationsAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "README.md"), "# fine\na checkpoint baseline\n")
	write(t, filepath.Join(dir, "docs", "story.md"), "has a snapshot inside\n")
	lister := func(root string) ([]string, error) {
		return []string{"README.md", "docs/story.md"}, nil
	}
	var out bytes.Buffer
	code := run([]string{"-root", dir}, &out, lister)
	if code != 1 {
		t.Fatalf("run exit = %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "docs/story.md:1: glossary/checkpoint") {
		t.Errorf("output missing violation line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 violation") {
		t.Errorf("output missing summary:\n%s", out.String())
	}
}

func TestRunCleanExitsZero(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "README.md"), "checkpoints and merges only\n")
	lister := func(string) ([]string, error) { return []string{"README.md"}, nil }
	var out bytes.Buffer
	if code := run([]string{"-root", dir}, &out, lister); code != 0 {
		t.Fatalf("run exit = %d, want 0; output:\n%s", code, out.String())
	}
}

func TestRunListerFailureExitsTwo(t *testing.T) {
	boom := func(string) ([]string, error) { return nil, errors.New("no git") }
	var out bytes.Buffer
	if code := run(nil, &out, boom); code != 2 {
		t.Fatalf("run exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "no git") {
		t.Errorf("output missing error:\n%s", out.String())
	}
}

func TestGitListFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	sh(t, dir, "git", "init", "-q")
	sh(t, dir, "git", "checkout", "-q", "-b", "tmp")
	write(t, filepath.Join(dir, "one.md"), "x\n")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "two.go"), "package sub\n")
	sh(t, dir, "git", "add", "one.md", "sub")

	paths, err := gitListFiles(dir)
	if err != nil {
		t.Fatalf("gitListFiles: %v", err)
	}
	want := []string{"one.md", "sub/two.go"}
	if !slices.Equal(paths, want) {
		t.Errorf("gitListFiles = %v, want %v", paths, want)
	}

	if _, err := gitListFiles(t.TempDir()); err == nil {
		t.Error("gitListFiles on a non-repo dir succeeded, want error")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sh(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

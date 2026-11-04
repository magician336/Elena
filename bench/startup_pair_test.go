package bench

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestRemoveHintFilesTargetsHintNamesOnly pins the environment
// preparation step of the no-hint mode: exactly the files named after
// the engine's hint convention (<id>.hint) leave the directory; data
// files and everything else stay untouched.
func TestRemoveHintFilesTargetsHintNamesOnly(t *testing.T) {
	dir := t.TempDir()
	files := []string{"0000000001.hint", "0000000002.hint", "0000000001.log", "0000000002.log", "lock"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}
	removed, err := removeHintFiles(dir)
	if err != nil {
		t.Fatalf("removeHintFiles: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed %d files, want 2", removed)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	left := make([]string, len(entries))
	for i, e := range entries {
		left[i] = e.Name()
	}
	slices.Sort(left)
	want := []string{"0000000001.log", "0000000002.log", "lock"}
	if !slices.Equal(left, want) {
		t.Fatalf("directory holds %v after removal, want %v", left, want)
	}
}

// TestRemoveHintFilesMissingDir pins the failure path: a directory that
// cannot be listed is an error, not a silent zero.
func TestRemoveHintFilesMissingDir(t *testing.T) {
	if _, err := removeHintFiles(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("removeHintFiles of a missing directory succeeded, want error")
	}
}

// TestRunStartupPairMicro drives the scenario end to end at micro
// scale: the same seeded dataset opens twice — once with the hint
// files Close wrote, once with them removed — and each mode carries
// its own Open timing plus a verified first read.
func TestRunStartupPairMicro(t *testing.T) {
	cfg := StartupPairConfig{Sessions: 4, EventsPerSession: 8, ValueSize: 256}
	got, err := RunStartupPair(filepath.Join(t.TempDir(), "data"), cfg)
	if err != nil {
		t.Fatalf("RunStartupPair: %v", err)
	}
	for _, mode := range []struct {
		name   string
		timing StartupTiming
	}{
		{"hint", got.Hint},
		{"no-hint", got.NoHint},
	} {
		if mode.timing.Mode != mode.name {
			t.Errorf("%s timing mode = %q, want %q", mode.name, mode.timing.Mode, mode.name)
		}
		if mode.timing.OpenNS <= 0 {
			t.Errorf("%s OpenNS = %d, want positive", mode.name, mode.timing.OpenNS)
		}
		if mode.timing.FirstReadNS < 0 {
			t.Errorf("%s FirstReadNS = %d, want non-negative", mode.name, mode.timing.FirstReadNS)
		}
		if mode.timing.LiveEntries == 0 || mode.timing.DataBytes == 0 {
			t.Errorf("%s dataset facts = (%d entries, %d bytes), want both positive",
				mode.name, mode.timing.LiveEntries, mode.timing.DataBytes)
		}
	}
	if got.Hint.LiveEntries != got.NoHint.LiveEntries {
		t.Fatalf("live entries differ between modes: hint %d, no-hint %d, want one equal index",
			got.Hint.LiveEntries, got.NoHint.LiveEntries)
	}
	if got.Hint.DataBytes != got.NoHint.DataBytes {
		t.Fatalf("data bytes differ between modes: hint %d, no-hint %d, want one dataset",
			got.Hint.DataBytes, got.NoHint.DataBytes)
	}
}

// TestRunStartupPairRejectsReuse pins the fresh-directory contract.
func TestRunStartupPairRejectsReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	cfg := StartupPairConfig{Sessions: 2, EventsPerSession: 2, ValueSize: 64}
	if _, err := RunStartupPair(dir, cfg); err != nil {
		t.Fatalf("first RunStartupPair: %v", err)
	}
	if _, err := RunStartupPair(dir, cfg); err == nil {
		t.Fatal("second RunStartupPair over the same dir succeeded, want failure")
	}
}

// TestRunStartupPairValidatesConfig pins the declared shape's guards.
func TestRunStartupPairValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  StartupPairConfig
	}{
		{"zero sessions", StartupPairConfig{Sessions: 0, EventsPerSession: 1, ValueSize: 1}},
		{"zero events", StartupPairConfig{Sessions: 1, EventsPerSession: 0, ValueSize: 1}},
		{"zero value size", StartupPairConfig{Sessions: 1, EventsPerSession: 1, ValueSize: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunStartupPair(filepath.Join(t.TempDir(), "data"), tc.cfg); err == nil {
				t.Fatal("RunStartupPair accepted an invalid config, want error")
			}
		})
	}
}

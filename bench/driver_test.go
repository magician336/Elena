package bench

import (
	"errors"
	"testing"
	"time"
)

// TestDriversListsRegistry pins the registry's report order: every
// engine the harness drives, exactly once, by the name For resolves.
func TestDriversListsRegistry(t *testing.T) {
	want := []string{"elena", "bbolt", "badger", "rosedb"}
	got := Drivers()
	if len(got) != len(want) {
		t.Fatalf("Drivers() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("Drivers()[%d] = %q, want %q", i, got[i], name)
		}
	}
}

// TestForTierPerWriteBuildsEveryEngine pins the per-write tier as the
// one every engine aligns on: each name resolves to a fresh driver
// under its default synchronized regime.
func TestForTierPerWriteBuildsEveryEngine(t *testing.T) {
	for _, name := range Drivers() {
		d, err := ForTier(name, SyncPerWrite, 0)
		if err != nil {
			t.Fatalf("ForTier(%s, SyncPerWrite): %v", name, err)
		}
		if d.Name() != name {
			t.Fatalf("driver name = %q, want %q", d.Name(), name)
		}
	}
}

// TestForTierGroupCommitOnlyElena pins the alignment boundary: the
// group-commit tier exists only where an engine exposes a periodic-sync
// configuration — elena — and every engine without one is an
// ErrTierUnsupported, the combination the matrix skips loudly instead
// of misreporting.
func TestForTierGroupCommitOnlyElena(t *testing.T) {
	for _, name := range Drivers() {
		d, err := ForTier(name, SyncGroupCommit, time.Second)
		if name == "elena" {
			if err != nil {
				t.Fatalf("ForTier(elena, SyncGroupCommit): %v", err)
			}
			if d == nil || d.Name() != "elena" {
				t.Fatalf("driver = %v, want elena", d)
			}
			continue
		}
		if err == nil {
			t.Fatalf("ForTier(%s, SyncGroupCommit) succeeded, want ErrTierUnsupported", name)
		}
		if !errors.Is(err, ErrTierUnsupported) {
			t.Fatalf("ForTier(%s, SyncGroupCommit) error = %v, want one wrapping ErrTierUnsupported", name, err)
		}
	}
}

// TestForTierGroupCommitRequiresPeriod pins the period contract of the
// group-commit tier: a non-positive period is a configuration error at
// construction, before any store opens.
func TestForTierGroupCommitRequiresPeriod(t *testing.T) {
	for _, period := range []time.Duration{0, -time.Second} {
		if _, err := ForTier("elena", SyncGroupCommit, period); err == nil {
			t.Fatalf("ForTier(elena, SyncGroupCommit, %v) succeeded, want error", period)
		}
	}
}

// TestForTierUnknownEngineFails pins the registry boundary: an unknown
// name is an error under every tier, never a default.
func TestForTierUnknownEngineFails(t *testing.T) {
	for _, name := range []string{"", "pebble", "elena "} {
		if _, err := ForTier(name, SyncPerWrite, 0); err == nil {
			t.Fatalf("ForTier(%q, SyncPerWrite) succeeded, want error", name)
		}
	}
}

// TestSyncTierString pins the tier names the reports print.
func TestSyncTierString(t *testing.T) {
	tests := []struct {
		tier SyncTier
		want string
	}{
		{SyncPerWrite, "per-write-sync"},
		{SyncGroupCommit, "group-commit"},
		{SyncTier(99), "sync-tier(99)"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Fatalf("SyncTier(%d).String() = %q, want %q", int(tt.tier), got, tt.want)
		}
	}
}

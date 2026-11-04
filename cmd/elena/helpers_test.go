package main

import (
	"testing"
	"time"

	"elena"
)

// seedDir builds a store through the public library API and returns its
// directory: two namespaces, three sessions, three events — the shared
// fixture the command tests read back through the CLI.
//
//	alpha/runner   perpetual, events "first", "second"
//	alpha/scratch  expires in an hour, event "note"
//	beta/empty     perpetual, no events
func seedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedInto(t, dir)
	return dir
}

// seedInto builds the fixture store inside dir.
func seedInto(t *testing.T, dir string) {
	t.Helper()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open seeded store: %v", err)
	}
	defer store.Close()

	runner, err := store.CreateSession("alpha", "runner")
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	for _, payload := range []string{"first", "second"} {
		if _, err := runner.Append([]byte(payload)); err != nil {
			t.Fatalf("append %q: %v", payload, err)
		}
	}
	scratch, err := store.CreateSessionWithTTL("alpha", "scratch", time.Hour)
	if err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	if _, err := scratch.Append([]byte("note")); err != nil {
		t.Fatalf("append note: %v", err)
	}
	if _, err := store.CreateSession("beta", "empty"); err != nil {
		t.Fatalf("create empty: %v", err)
	}
}

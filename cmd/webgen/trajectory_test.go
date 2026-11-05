package main

import (
	"slices"
	"testing"

	"elena"
)

// trajectoryVocabulary is the fixed pool the seeded selection draws
// event payloads from, declared here independently of the generator:
// every emitted payload must be one of these lines, none repeated.
var trajectoryVocabulary = []string{
	"session opened",
	"context loaded: 12 files",
	"tool result: schema mapped",
	"tool result: 3 matches",
	"tool result: page fetched",
	"tool result: rows decoded",
	"plan revised: 2 steps",
	"plan revised: 5 steps",
	"draft updated: 240 words",
	"draft updated: outline",
	"review note: edge case found",
	"answer drafted",
}

// TestTrajectoryStoryRunsTheScript drives the trajectory story through
// the public library API against a real store and asserts the emitted
// structure: a fixed namespace and session, six seeded appends whose
// sequence numbers run 1..6, timestamps read off the injected clock in
// fixed 7s steps, and a point-in-time read that stops at seq 3.
func TestTrajectoryStoryRunsTheScript(t *testing.T) {
	store, err := elena.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	story, err := trajectoryStory(store, newFakeClock(), 1)
	if err != nil {
		t.Fatalf("trajectoryStory: %v", err)
	}

	if story.Namespace != "runs" || story.Session != "alpha" {
		t.Fatalf("namespace/session = %q/%q, want runs/alpha", story.Namespace, story.Session)
	}
	if story.CreatedAt != "2026-01-05T09:00:00Z" {
		t.Fatalf("created_at = %s, want the clock start 2026-01-05T09:00:00Z", story.CreatedAt)
	}
	if len(story.Events) != 6 {
		t.Fatalf("got %d events, want 6", len(story.Events))
	}
	wantTimestamps := []string{
		"2026-01-05T09:00:07Z", "2026-01-05T09:00:14Z", "2026-01-05T09:00:21Z",
		"2026-01-05T09:00:28Z", "2026-01-05T09:00:35Z", "2026-01-05T09:00:42Z",
	}
	for i, ev := range story.Events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
		if ev.At != wantTimestamps[i] {
			t.Fatalf("event %d timestamp %s, want %s", i, ev.At, wantTimestamps[i])
		}
		if !slices.Contains(trajectoryVocabulary, ev.Payload) {
			t.Fatalf("event %d payload %q is not from the vocabulary", i, ev.Payload)
		}
	}
	if story.PointInTime.Seq != 3 || story.PointInTime.Returned != 3 {
		t.Fatalf("point-in-time = %+v, want seq 3 returning 3 events", story.PointInTime)
	}
	if story.PointInTime.At != "2026-01-05T09:00:49Z" {
		t.Fatalf("point-in-time timestamp %s, want 2026-01-05T09:00:49Z", story.PointInTime.At)
	}
}

// TestSeededPayloadsAreSelectedBySeed pins the seed's observable
// effect: one seed picks a fixed permutation of the vocabulary, two
// runs of the same seed agree, and a different seed picks a different
// list.
func TestSeededPayloadsAreSelectedBySeed(t *testing.T) {
	first := seededEventPayloads(1)
	second := seededEventPayloads(1)
	if !slices.Equal(first, second) {
		t.Fatalf("same seed gave different lists: %q vs %q", first, second)
	}
	other := seededEventPayloads(2)
	if slices.Equal(first, other) {
		t.Fatalf("seeds 1 and 2 gave the same list %q; the seed must drive the selection", first)
	}
	for _, payload := range first {
		count := 0
		for _, candidate := range first {
			if candidate == payload {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("payload %q appears %d times; seeded selection must not repeat", payload, count)
		}
	}
}

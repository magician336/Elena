package main

import (
	"fmt"
	"math/rand/v2"

	"github.com/magician336/elena"
)

// eventVocabulary is the fixed pool the seed draws the trajectory
// story's payloads from — plain working-session lines, the register
// the page's one agent scene line shares.
var eventVocabulary = []string{
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

// trajectoryEventCount is how many events the story appends; the
// point-in-time read stops at half of them.
const trajectoryEventCount = 6

// seededEventPayloads returns count distinct payloads the seed picked
// from the vocabulary, in pick order: the same seed always picks the
// same list, byte for byte, and a different seed picks a different
// one.
func seededEventPayloads(seed uint64) []string {
	picked := make([]string, len(eventVocabulary))
	copy(picked, eventVocabulary)
	r := rand.New(rand.NewPCG(seed, seed^0x656c656e61))
	r.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	return picked[:trajectoryEventCount]
}

// trajectoryStory narrates the first act against a live store through
// the public library API: session creation, six seeded appends with
// their handed-out sequence numbers, and one point-in-time read to
// half the trajectory. Every timestamp is the injected clock's
// reading — the store's own append times are wall-clock readings and
// deliberately never enter the output.
func trajectoryStory(store *elena.Store, clk *fakeClock, seed uint64) (TrajectoryStory, error) {
	const namespace, session = "runs", "alpha"
	story := TrajectoryStory{Namespace: namespace, Session: session, CreatedAt: clk.Now().UTC().Format(timestampFormat)}


	sess, err := store.CreateSession(namespace, session)
	if err != nil {
		return TrajectoryStory{}, fmt.Errorf("session create: %w", err)
	}
	story.Events = make([]StoryEvent, 0, trajectoryEventCount)
	for _, payload := range seededEventPayloads(seed) {
		seq, err := sess.Append([]byte(payload))
		if err != nil {
			return TrajectoryStory{}, fmt.Errorf("append %q: %w", payload, err)
		}
		story.Events = append(story.Events, StoryEvent{Seq: seq, At: clk.tick(), Payload: payload})
	}

	bound := story.Events[trajectoryEventCount/2-1].Seq
	past, err := sess.EventsUpTo(bound)
	if err != nil {
		return TrajectoryStory{}, fmt.Errorf("point-in-time read to seq %d: %w", bound, err)
	}
	story.PointInTime = PointInTimeView{Seq: bound, At: clk.tick(), Returned: len(past)}
	return story, nil
}

package main

import "time"

// clockStart and clockStep are the injected clock's fixed constants:
// every timestamp in the generated page data is clockStart plus a
// whole number of clockStep advances — no wall-clock reading ever
// enters the output, which is half of the byte-equal determinism
// guarantee (the seed driving the payloads is the other half).
// clockStart is not a compile-time constant (a time.Time cannot be)
// but is never assigned to: it is read-only by convention, like a
// const.
var clockStart = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

const (
	clockStep = 7 * time.Second

	// timestampFormat is the one format every timestamp in the page
	// data uses.
	timestampFormat = time.RFC3339
)

// fakeClock is the story generator's injected clock. It is not safe
// for concurrent use; the stories run single-threaded by design.
type fakeClock struct {
	now time.Time
}

// newFakeClock returns a clock reading clockStart.
func newFakeClock() *fakeClock {
	return &fakeClock{now: clockStart}
}

// Now returns the current injected reading.
func (c *fakeClock) Now() time.Time {
	return c.now
}

// Advance moves the clock one step forward.
func (c *fakeClock) Advance(step time.Duration) {
	c.now = c.now.Add(step)
}

// tick advances the clock one step and returns the new reading,
// formatted the way the page data formats every timestamp.
func (c *fakeClock) tick() string {
	c.Advance(clockStep)
	return c.Now().UTC().Format(time.RFC3339)
}

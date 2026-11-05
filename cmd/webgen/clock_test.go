package main

import (
	"testing"
	"time"
)

// TestFakeClockIsDeterministic pins the injected clock's contract: it
// starts at a fixed instant, advances by exactly the given step, and
// never consults the wall clock — the timestamps in the generated
// page data all come from it.
func TestFakeClockIsDeterministic(t *testing.T) {
	clk := newFakeClock()
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	if !clk.Now().Equal(start) {
		t.Fatalf("clock starts at %s, want %s", clk.Now(), start)
	}
	clk.Advance(7 * time.Second)
	second := clk.Now()
	if want := start.Add(7 * time.Second); !second.Equal(want) {
		t.Fatalf("after one 7s step clock reads %s, want %s", second, want)
	}
	clk.Advance(7 * time.Second)
	if want := start.Add(14 * time.Second); !clk.Now().Equal(want) {
		t.Fatalf("after two 7s steps clock reads %s, want %s", clk.Now(), want)
	}
}

// TestTwoClocksAgree proves two fresh clocks with no seeds of their
// own produce identical readings — the determinism the byte-equal
// generation test leans on.
func TestTwoClocksAgree(t *testing.T) {
	a, b := newFakeClock(), newFakeClock()
	for i := 0; i < 5; i++ {
		a.Advance(7 * time.Second)
		b.Advance(7 * time.Second)
	}
	if !a.Now().Equal(b.Now()) {
		t.Fatalf("two fresh clocks diverged: %s vs %s", a.Now(), b.Now())
	}
}

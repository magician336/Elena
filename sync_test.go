package elena_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/magician336/elena"
)

// WithSyncInterval must reject periods that cannot define a flush
// interval — zero or negative — with ErrInvalidSyncPeriod, and hand
// back no store, leaving the directory free to open afterwards.
func TestOpenWithSyncIntervalRejectsInvalidPeriod(t *testing.T) {
	dir := t.TempDir()

	for _, period := range []time.Duration{0, -time.Second} {
		st, err := elena.Open(dir, elena.WithSyncInterval(period))
		if !errors.Is(err, elena.ErrInvalidSyncPeriod) {
			t.Errorf("Open(WithSyncInterval(%v)) err = %v, want ErrInvalidSyncPeriod", period, err)
		}
		if st != nil {
			closeStore(t, st)
			t.Error("Open must not return a store on failure")
		}
	}

	// The rejected opens must not have locked the directory.
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) after rejected opens failed: %v", dir, err)
	}
	closeStore(t, st)
}

// Every sync tier must keep one recovery contract through the facade:
// whatever was acknowledged before Close is exactly the live set a
// reopen rebuilds — the tier trades the durability window, never the
// recovered content of a cleanly closed store. The interval row pins a
// period far longer than the whole test body, so no flush tick ever
// fires and Close's forced final flush is the only durability path the
// acknowledged events could have taken; the none row samples that a
// reopened directory still rebuilds a consistent index even though its
// data entries were never fdatasync'ed.
func TestSyncTiersAcknowledgeAndRecoverAcrossReopen(t *testing.T) {
	const ttl = 300 * time.Millisecond
	for _, tt := range []struct {
		name string
		opts []elena.OpenOption
	}{
		{name: "default", opts: nil},
		{name: "always", opts: []elena.OpenOption{elena.WithSyncAlways()}},
		{name: "interval", opts: []elena.OpenOption{elena.WithSyncInterval(time.Hour)}},
		{name: "none", opts: []elena.OpenOption{elena.WithSyncNone()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := elena.Open(dir, tt.opts...)
			if err != nil {
				t.Fatalf("Open(%q, %s) failed: %v", dir, tt.name, err)
			}

			keeper := createSession(t, st, "runs", "keeper")
			for i := 1; i <= 3; i++ {
				mustAppend(t, keeper, fmt.Sprintf("event-%d", i))
			}
			temp := createTimedSession(t, st, "runs", "temp", ttl)
			for i := 1; i <= 2; i++ {
				mustAppend(t, temp, "temporary")
			}

			// Everything acknowledged reads back before Close...
			events, err := keeper.Events()
			if err != nil {
				t.Fatalf("Events() before Close failed: %v", err)
			}
			if len(events) != 3 {
				t.Fatalf("Events() before Close returned %d events, want 3", len(events))
			}

			if err := st.Close(); err != nil {
				t.Fatalf("Close under the %s tier failed: %v", tt.name, err)
			}
			waitForTTLElapse(ttl)

			// ...and is exactly the live set after reopen under the same
			// tier: the perpetual trajectory intact and hole-free, the
			// elapsed timed session gone, the sequence continuing.
			reopened, err := elena.Open(dir, tt.opts...)
			if err != nil {
				t.Fatalf("reopen under the %s tier failed: %v", tt.name, err)
			}
			defer closeStore(t, reopened)

			resumed := createSession(t, reopened, "runs", "keeper")
			events, err = resumed.Events()
			if err != nil {
				t.Fatalf("Events() after reopen failed: %v", err)
			}
			for i, ev := range events {
				if ev.Seq != uint64(i+1) || ev.Payload == nil || string(ev.Payload) != fmt.Sprintf("event-%d", i+1) {
					t.Fatalf("event %d after reopen = (seq %d, payload %q), want (%d, \"event-%d\")", i, ev.Seq, ev.Payload, i+1, i+1)
				}
			}
			if len(events) != 3 {
				t.Fatalf("Events() after reopen returned %d events, want 3", len(events))
			}
			if seq, err := resumed.Append([]byte("post-reopen")); err != nil || seq != 4 {
				t.Errorf("Append() after reopen = (%d, %v), want (4, nil)", seq, err)
			}

			sessions, err := reopened.Sessions("runs")
			if err != nil {
				t.Fatalf("Sessions() after reopen failed: %v", err)
			}
			if len(sessions) != 1 || sessions[0].ID != "keeper" {
				t.Fatalf("Sessions() after reopen = %+v, want only the perpetual session \"keeper\"", sessions)
			}
			elapsed, err := reopened.CreateSessionWithTTL("runs", "temp", ttl)
			if err != nil {
				t.Fatalf("CreateSessionWithTTL of the elapsed name failed: %v", err)
			}
			if events, err := elapsed.Events(); err != nil || len(events) != 0 {
				t.Errorf("Events() of the elapsed session after reopen = (%d events, %v), want (0, nil)", len(events), err)
			}
		})
	}
}

// Sync options apply in the order given, so the last one wins — and
// the interval tier's period validation lands with it: a non-positive
// period is rejected only when interval is the tier the store will
// actually use, and a later always or none option overrides an invalid
// interval one entirely.
func TestSyncOptionsLastOneWins(t *testing.T) {
	for _, tt := range []struct {
		name    string
		opts    []elena.OpenOption
		wantErr bool
	}{
		{
			name: "invalid-interval-then-always",
			opts: []elena.OpenOption{elena.WithSyncInterval(0), elena.WithSyncAlways()},
		},
		{
			name: "invalid-interval-then-none",
			opts: []elena.OpenOption{elena.WithSyncInterval(-time.Second), elena.WithSyncNone()},
		},
		{
			name: "valid-interval-then-always",
			opts: []elena.OpenOption{elena.WithSyncInterval(time.Hour), elena.WithSyncAlways()},
		},
		{
			name:    "always-then-invalid-interval",
			opts:    []elena.OpenOption{elena.WithSyncAlways(), elena.WithSyncInterval(0)},
			wantErr: true,
		},
		{
			name:    "none-then-invalid-interval",
			opts:    []elena.OpenOption{elena.WithSyncNone(), elena.WithSyncInterval(-time.Second)},
			wantErr: true,
		},
		{
			name:    "valid-interval-then-invalid-interval",
			opts:    []elena.OpenOption{elena.WithSyncInterval(time.Hour), elena.WithSyncInterval(0)},
			wantErr: true,
		},
		{
			name: "invalid-interval-then-valid-interval",
			opts: []elena.OpenOption{elena.WithSyncInterval(0), elena.WithSyncInterval(time.Hour)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st, err := elena.Open(t.TempDir(), tt.opts...)
			if tt.wantErr {
				if !errors.Is(err, elena.ErrInvalidSyncPeriod) {
					t.Errorf("Open err = %v, want ErrInvalidSyncPeriod", err)
				}
				if st != nil {
					closeStore(t, st)
					t.Error("Open must not return a store on failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			closeStore(t, st)
		})
	}
}

// The sync policy is a property of one Open, never of the directory:
// nothing tier-shaped is encoded on disk, so a directory written under
// one tier reopens under any other and every acknowledged event reads
// back through each of them.
func TestSyncTierChoiceIsPerOpenNotPerDirectory(t *testing.T) {
	dir := t.TempDir()

	st, err := elena.Open(dir, elena.WithSyncInterval(time.Hour))
	if err != nil {
		t.Fatalf("Open under interval failed: %v", err)
	}
	underInterval := createSession(t, st, "runs", "writer")
	mustAppend(t, underInterval, "event-1")
	mustAppend(t, underInterval, "event-2")
	if err := st.Close(); err != nil {
		t.Fatalf("Close under interval failed: %v", err)
	}

	underNone, err := elena.Open(dir, elena.WithSyncNone())
	if err != nil {
		t.Fatalf("reopen under none failed: %v", err)
	}
	resumed := createSession(t, underNone, "runs", "writer")
	events, err := resumed.Events()
	if err != nil || len(events) != 2 {
		t.Fatalf("Events() under none = (%d events, %v), want (2, nil)", len(events), err)
	}
	mustAppend(t, resumed, "event-3")
	if err := underNone.Close(); err != nil {
		t.Fatalf("Close under none failed: %v", err)
	}

	underDefault, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("reopen under the default tier failed: %v", err)
	}
	defer closeStore(t, underDefault)
	final := createSession(t, underDefault, "runs", "writer")
	events, err = final.Events()
	if err != nil {
		t.Fatalf("Events() under the default tier failed: %v", err)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d under the default tier has sequence %d, want %d", i, ev.Seq, i+1)
		}
	}
	if len(events) != 3 {
		t.Fatalf("Events() under the default tier returned %d events, want 3", len(events))
	}
}

// The interval tier's flusher and the background sweep are two
// goroutines over one store: deadlines elapse, the sweep merges, the
// flusher keeps syncing the active file the merge rotates in and out
// from under it, and Close joins both before releasing the directory —
// the reopen right after Close is the observable proof of the joins.
func TestSweepRunsUnderSyncInterval(t *testing.T) {
	dir := t.TempDir()
	st, err := elena.Open(dir,
		elena.WithSyncInterval(time.Millisecond),
		elena.WithSweepPeriod(25*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Open(interval, sweep) failed: %v", err)
	}

	const ttl = 300 * time.Millisecond
	temp := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 3; i++ {
		mustAppend(t, temp, "event")
	}

	waitForStats(t, st, "the sweep to reclaim the elapsed session under interval", func(s elena.Stats) bool {
		return s.LiveEntries == 0 && s.DataBytes == 0
	})

	if err := st.Close(); err != nil {
		t.Fatalf("Close under interval with the sweep running failed: %v", err)
	}
	reopened, err := elena.Open(dir, elena.WithSyncInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("Open right after Close failed: %v — a background goroutine outlived its store", err)
	}
	closeStore(t, reopened)
}

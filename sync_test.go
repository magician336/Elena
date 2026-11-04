package elena_test

import (
	"errors"
	"testing"
	"time"

	"elena"
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

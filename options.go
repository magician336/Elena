package elena

import (
	"fmt"
	"time"

	"elena/internal/semantic"
)

// DefaultSweepPeriod is the period of the background sweep when Open is
// called without an explicit sweep option: the sweep checks its deadline
// view once per minute by default.
const DefaultSweepPeriod = semantic.DefaultSweepPeriod

// OpenOption configures a store at Open time. Options apply in the
// order given; where two options configure the same behavior, the last
// one wins.
type OpenOption func(*openOptions)

// openOptions accumulates the Open options into the semantic layer's
// configuration. sweepPeriodSet distinguishes "caller asked for this
// period" from "caller said nothing", so an explicit zero is rejected
// instead of being read as the default.
type openOptions struct {
	sweepPeriod    time.Duration
	sweepPeriodSet bool
	sweepDisabled  bool
}

// config validates the accumulated options and returns the semantic
// layer's configuration they amount to.
func (o *openOptions) config() (semantic.Config, error) {
	if o.sweepPeriodSet && o.sweepPeriod <= 0 {
		return semantic.Config{}, fmt.Errorf("sweep period %d: %w", o.sweepPeriod, ErrInvalidSweepPeriod)
	}
	return semantic.Config{
		SweepPeriod:   o.sweepPeriod,
		SweepDisabled: o.sweepDisabled,
	}, nil
}

// WithSweepPeriod sets the period of the background sweep: once per
// period the sweep checks its deadline view of the timed sessions and,
// when a deadline has elapsed, triggers one merge — the merge rewrites
// the live entries and omits the expired ones, returning their disk
// bytes and index memory. The period must be positive (an error
// wrapping ErrInvalidSweepPeriod from Open otherwise).
func WithSweepPeriod(period time.Duration) OpenOption {
	return func(o *openOptions) {
		o.sweepPeriod = period
		o.sweepPeriodSet = true
		o.sweepDisabled = false
	}
}

package bench

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// Latency collects raw per-operation latencies. The samples are kept in
// observation order and in full resolution, so downstream summaries —
// and the percentile math built on this collector — always read the
// original measurements, never a lossy running aggregate.
type Latency struct {
	samples []time.Duration
}

// Observe records one operation's latency.
func (l *Latency) Observe(d time.Duration) {
	l.samples = append(l.samples, d)
}

// Summarize returns the point-in-time summary of the observed samples:
// their count, minimum, maximum, mean, and the P50/P99/P999 percentile
// trio reports print. An empty collector summarizes to the zero value.
func (l *Latency) Summarize() LatencySummary {
	if len(l.samples) == 0 {
		return LatencySummary{}
	}
	var total time.Duration
	min, max := l.samples[0], l.samples[0]
	for _, s := range l.samples {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
		total += s
	}
	sorted := sortedCopy(l.samples)
	return LatencySummary{
		Count: len(l.samples),
		Min:   min,
		Max:   max,
		Mean:  total / time.Duration(len(l.samples)),
		P50:   quantileOfSorted(sorted, 0.5),
		P99:   quantileOfSorted(sorted, 0.99),
		P999:  quantileOfSorted(sorted, 0.999),
	}
}

// Samples returns a copy of the raw observations in observation order.
func (l *Latency) Samples() []time.Duration {
	out := make([]time.Duration, len(l.samples))
	copy(out, l.samples)
	return out
}

// LatencySummary is an immutable summary of a Latency collector: the
// per-phase numbers reports print. Percentiles are the report language;
// the mean stays for continuity checks but no report leads with it —
// tail latency is the claim, averages flatten it.
type LatencySummary struct {
	Count int
	Min   time.Duration
	Max   time.Duration
	Mean  time.Duration
	P50   time.Duration
	P99   time.Duration
	P999  time.Duration
}

// Quantile returns the p-quantile of samples by the nearest-rank
// method: the samples rank ascending and the quantile is the one at
// rank ceil(p*n) — always an observed sample, never an interpolated
// value between two. Samples too few for the rank to resolve below the
// maximum (P999 of ten samples) degrade to the maximum, which the rank
// arithmetic makes explicit rather than inventing data. p must lie in
// (0, 1] and samples must be non-empty. The input slice is not
// modified.
func Quantile(samples []time.Duration, p float64) (time.Duration, error) {
	if len(samples) == 0 {
		return 0, fmt.Errorf("quantile of no samples")
	}
	if p <= 0 || p > 1 {
		return 0, fmt.Errorf("quantile p %g out of (0, 1]", p)
	}
	return quantileOfSorted(sortedCopy(samples), p), nil
}

// quantileOfSorted reads the nearest-rank quantile off an ascending
// slice: rank ceil(p*n), 1-based, into the sorted samples.
func quantileOfSorted(sorted []time.Duration, p float64) time.Duration {
	rank := p * float64(len(sorted))
	// Rank arithmetic carries float rounding: a true integer rank can
	// land a hair above itself and round one place too high. Shaving a
	// relative 1e-14 — two orders above the product's own rounding, far
	// below any true fractional part at realistic sample counts — keeps
	// ceil on the exact side.
	rank -= math.Abs(rank) * 1e-14
	r := int(math.Ceil(rank))
	if r < 1 {
		r = 1
	}
	if r > len(sorted) {
		r = len(sorted)
	}
	return sorted[r-1]
}

// sortedCopy returns an ascending copy of samples: summaries rank
// privately, never reordering the caller's observation stream.
func sortedCopy(samples []time.Duration) []time.Duration {
	out := make([]time.Duration, len(samples))
	copy(out, samples)
	slices.Sort(out)
	return out
}

// Throughput returns operations per second — count over elapsed. A
// non-positive elapsed yields zero, not a division panic: a phase that
// measured no time reports no rate.
func Throughput(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

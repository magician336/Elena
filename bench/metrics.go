package bench

import "time"

// Latency collects raw per-operation latencies. The samples are kept in
// observation order and in full resolution, so downstream summaries —
// and the percentile math later work builds on this collector — always
// read the original measurements, never a lossy running aggregate.
type Latency struct {
	samples []time.Duration
}

// Observe records one operation's latency.
func (l *Latency) Observe(d time.Duration) {
	l.samples = append(l.samples, d)
}

// Summarize returns the point-in-time summary of the observed samples:
// their count, minimum, maximum, and mean. An empty collector summarizes
// to the zero value.
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
	return LatencySummary{
		Count: len(l.samples),
		Min:   min,
		Max:   max,
		Mean:  total / time.Duration(len(l.samples)),
	}
}

// Samples returns a copy of the raw observations in observation order.
func (l *Latency) Samples() []time.Duration {
	out := make([]time.Duration, len(l.samples))
	copy(out, l.samples)
	return out
}

// LatencySummary is an immutable summary of a Latency collector: the
// per-phase numbers reports print and later percentile work derives
// from, alongside the raw samples.
type LatencySummary struct {
	Count int
	Min   time.Duration
	Max   time.Duration
	Mean  time.Duration
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

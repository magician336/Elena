package bench

import (
	"reflect"
	"testing"
	"time"
)

// TestLatencySummarize pins the summary math on known samples: the
// collector is the observability seam every phase reports through, so
// its count, min, max, and mean must be exact on worked examples.
func TestLatencySummarize(t *testing.T) {
	tests := []struct {
		name    string
		samples []time.Duration
		want    LatencySummary
	}{
		{
			name: "no samples yield the zero summary",
			want: LatencySummary{},
		},
		{
			name:    "one sample is min max mean and every percentile",
			samples: []time.Duration{2 * time.Millisecond},
			want: LatencySummary{Count: 1, Min: 2 * time.Millisecond, Max: 2 * time.Millisecond, Mean: 2 * time.Millisecond,
				P50: 2 * time.Millisecond, P99: 2 * time.Millisecond, P999: 2 * time.Millisecond},
		},
		{
			name:    "unordered samples span min to max",
			samples: []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond},
			want: LatencySummary{Count: 3, Min: 1 * time.Millisecond, Max: 3 * time.Millisecond, Mean: 2 * time.Millisecond,
				P50: 2 * time.Millisecond, P99: 3 * time.Millisecond, P999: 3 * time.Millisecond},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lat Latency
			for _, s := range tt.samples {
				lat.Observe(s)
			}
			if got := lat.Summarize(); got != tt.want {
				t.Fatalf("Summarize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLatencySamplesCopy pins immutability: the returned slice is a
// copy, so a caller bending the summary cannot corrupt later reads of
// the collector — the raw samples stay intact for the percentile math
// that later work builds on this seam.
func TestLatencySamplesCopy(t *testing.T) {
	var lat Latency
	lat.Observe(time.Millisecond)
	lat.Observe(2 * time.Millisecond)

	got := lat.Samples()
	if len(got) != 2 {
		t.Fatalf("Samples() length = %d, want 2", len(got))
	}
	got[0] = time.Hour

	if again := lat.Samples(); !reflect.DeepEqual(again, []time.Duration{time.Millisecond, 2 * time.Millisecond}) {
		t.Fatalf("internal samples mutated via returned slice: %v", again)
	}
}

// TestQuantile pins the percentile math on worked examples with the
// nearest-rank method: the quantile of n samples is the sample at rank
// ceil(p*n) of the ascending order — always an observed sample, never
// an interpolated value between two. Ranks are worked by hand here, so
// any change to the method breaks the pin, not the other way round.
func TestQuantile(t *testing.T) {
	tests := []struct {
		name    string
		samples []time.Duration
		p       float64
		want    time.Duration
	}{
		{
			name:    "median of ten samples is the fifth smallest",
			samples: []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			p:       0.5,
			want:    5,
		},
		{
			name:    "median of four samples is the second smallest, no midpoint",
			samples: []time.Duration{1, 2, 3, 4},
			p:       0.5,
			want:    2,
		},
		{
			name:    "unordered input is ranked ascending first",
			samples: []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond},
			p:       0.5,
			want:    2 * time.Millisecond,
		},
		{
			name:    "p99 of ten samples is the maximum",
			samples: []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			p:       0.99,
			want:    10,
		},
		{
			name:    "p999 of ten samples degrades to the maximum",
			samples: []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			p:       0.999,
			want:    10,
		},
		{
			name:    "p999 of a thousand samples is the 999th smallest",
			samples: seqDurations(1000, time.Microsecond),
			p:       0.999,
			want:    999 * time.Microsecond,
		},
		{
			name:    "all-equal samples quantify to that value at any p",
			samples: []time.Duration{7 * time.Millisecond, 7 * time.Millisecond, 7 * time.Millisecond},
			p:       0.99,
			want:    7 * time.Millisecond,
		},
		{
			name:    "one sample answers every quantile",
			samples: []time.Duration{9 * time.Millisecond},
			p:       0.999,
			want:    9 * time.Millisecond,
		},
		{
			name:    "p one is the maximum",
			samples: []time.Duration{4, 9, 2},
			p:       1,
			want:    9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Quantile(tt.samples, tt.p)
			if err != nil {
				t.Fatalf("Quantile(%v, %v): %v", tt.samples, tt.p, err)
			}
			if got != tt.want {
				t.Fatalf("Quantile(%v, %v) = %v, want %v", tt.samples, tt.p, got, tt.want)
			}
		})
	}
}

// TestQuantileRejects pins the error contract: no samples or a p
// outside (0, 1] has no rank to read.
func TestQuantileRejects(t *testing.T) {
	for _, tt := range []struct {
		name    string
		samples []time.Duration
		p       float64
	}{
		{name: "no samples", samples: nil, p: 0.5},
		{name: "p zero", samples: []time.Duration{time.Millisecond}, p: 0},
		{name: "negative p", samples: []time.Duration{time.Millisecond}, p: -0.5},
		{name: "p above one", samples: []time.Duration{time.Millisecond}, p: 1.01},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Quantile(tt.samples, tt.p); err == nil {
				t.Fatalf("Quantile(%v, %v) = %v, want error", tt.samples, tt.p, got)
			}
		})
	}
}

// TestQuantileDoesNotMutateInput pins purity: the caller's slice keeps
// its observation order, so summarizing never rewrites the sample
// stream other summaries still read.
func TestQuantileDoesNotMutateInput(t *testing.T) {
	samples := []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond}
	if _, err := Quantile(samples, 0.5); err != nil {
		t.Fatalf("Quantile: %v", err)
	}
	want := []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond}
	if !reflect.DeepEqual(samples, want) {
		t.Fatalf("input mutated: %v, want %v", samples, want)
	}
}

// seqDurations builds the ascending run 1*step, 2*step, ..., n*step —
// known samples with known ranks for the percentile pin above.
func seqDurations(n int, step time.Duration) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Duration(i+1) * step
	}
	return out
}

// TestThroughput pins the ops-per-second math: exact on a worked
// example, zero rather than a panic on a zero elapsed time.
func TestThroughput(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		elapsed time.Duration
		want    float64
	}{
		{name: "100 ops in 2 seconds", count: 100, elapsed: 2 * time.Second, want: 50},
		{name: "3 ops in 1.5 seconds", count: 3, elapsed: 1500 * time.Millisecond, want: 2},
		{name: "zero elapsed yields zero", count: 10, elapsed: 0, want: 0},
		{name: "negative elapsed yields zero", count: 10, elapsed: -time.Second, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Throughput(tt.count, tt.elapsed); got != tt.want {
				t.Fatalf("Throughput(%d, %v) = %v, want %v", tt.count, tt.elapsed, got, tt.want)
			}
		})
	}
}

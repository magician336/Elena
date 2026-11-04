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
			name:    "one sample is min max and mean",
			samples: []time.Duration{2 * time.Millisecond},
			want:    LatencySummary{Count: 1, Min: 2 * time.Millisecond, Max: 2 * time.Millisecond, Mean: 2 * time.Millisecond},
		},
		{
			name:    "unordered samples span min to max",
			samples: []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond},
			want:    LatencySummary{Count: 3, Min: 1 * time.Millisecond, Max: 3 * time.Millisecond, Mean: 2 * time.Millisecond},
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

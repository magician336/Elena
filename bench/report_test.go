package bench

import (
	"bytes"
	"testing"
	"time"
)

// goldenResults is one fixed result set with hand-set numbers: the
// golden pins below spell out its exact rendering, so every byte —
// column order, duration formatting, float formatting, and the
// percentile-only latency columns — is deliberate. The mean is set to
// a nonzero value that appears in neither rendering: reports lead with
// tail latency, not averages.
func goldenResults() []Result {
	return []Result{
		{
			Driver:   "elena",
			Workload: "micro",
			Phases: []PhaseResult{
				{
					Phase:      "append",
					Ops:        3,
					Elapsed:    3 * time.Millisecond,
					Throughput: 1000,
					Latency: LatencySummary{
						Count: 3,
						Min:   400 * time.Microsecond,
						Max:   900 * time.Microsecond,
						Mean:  600 * time.Microsecond,
						P50:   500 * time.Microsecond,
						P99:   900 * time.Microsecond,
						P999:  900 * time.Microsecond,
					},
				},
				{
					Phase:      "read",
					Ops:        2,
					Elapsed:    1 * time.Millisecond,
					Throughput: 2000,
					Latency: LatencySummary{
						Count: 2,
						Min:   400 * time.Microsecond,
						Max:   600 * time.Microsecond,
						Mean:  500 * time.Microsecond,
						P50:   400 * time.Microsecond,
						P99:   600 * time.Microsecond,
						P999:  600 * time.Microsecond,
					},
				},
			},
		},
		{
			Driver:   "bbolt",
			Workload: "micro",
			Phases: []PhaseResult{
				{
					Phase:      "append",
					Ops:        3,
					Elapsed:    2 * time.Millisecond,
					Throughput: 1500,
					Latency: LatencySummary{
						Count: 3,
						Min:   300 * time.Microsecond,
						Max:   800 * time.Microsecond,
						Mean:  500 * time.Microsecond,
						P50:   400 * time.Microsecond,
						P99:   800 * time.Microsecond,
						P999:  800 * time.Microsecond,
					},
				},
			},
		},
	}
}

// wantMarkdown is the golden rendering of goldenResults.
const wantMarkdown = `# Bench report

| driver | workload | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| elena | micro | append | 3 | 3ms | 1000.0 | 500µs | 900µs | 900µs | 400µs | 900µs |
| elena | micro | read | 2 | 1ms | 2000.0 | 400µs | 600µs | 600µs | 400µs | 600µs |
| bbolt | micro | append | 3 | 2ms | 1500.0 | 400µs | 800µs | 800µs | 300µs | 800µs |
`

// wantJSON is the golden JSON of goldenResults: fixed field names in
// fixed order, durations as nanosecond integers, one phase per object —
// the shape two runs of the same input diff against byte for byte.
const wantJSON = `{
  "results": [
    {
      "driver": "elena",
      "workload": "micro",
      "phases": [
        {
          "phase": "append",
          "ops": 3,
          "elapsed_ns": 3000000,
          "throughput_ops_sec": 1000,
          "latency": {
            "count": 3,
            "min_ns": 400000,
            "p50_ns": 500000,
            "p99_ns": 900000,
            "p999_ns": 900000,
            "max_ns": 900000
          }
        },
        {
          "phase": "read",
          "ops": 2,
          "elapsed_ns": 1000000,
          "throughput_ops_sec": 2000,
          "latency": {
            "count": 2,
            "min_ns": 400000,
            "p50_ns": 400000,
            "p99_ns": 600000,
            "p999_ns": 600000,
            "max_ns": 600000
          }
        }
      ]
    },
    {
      "driver": "bbolt",
      "workload": "micro",
      "phases": [
        {
          "phase": "append",
          "ops": 3,
          "elapsed_ns": 2000000,
          "throughput_ops_sec": 1500,
          "latency": {
            "count": 3,
            "min_ns": 300000,
            "p50_ns": 400000,
            "p99_ns": 800000,
            "p999_ns": 800000,
            "max_ns": 800000
          }
        }
      ]
    }
  ]
}
`

// TestRenderMarkdownGolden pins the markdown rendering byte for byte:
// one row per phase, latency as the P50/P99/P999 trio plus min and max,
// durations at microsecond resolution, throughput at one decimal — and
// no mean anywhere.
func TestRenderMarkdownGolden(t *testing.T) {
	if got := RenderMarkdown(goldenResults()); got != wantMarkdown {
		t.Fatalf("RenderMarkdown =\n%q\nwant\n%q", got, wantMarkdown)
	}
}

// TestRenderMarkdownDeterministic pins the reproducibility contract:
// the same results render to the identical bytes twice — no timestamps,
// no map iteration, no environment leaks into the output.
func TestRenderMarkdownDeterministic(t *testing.T) {
	results := goldenResults()
	first := RenderMarkdown(results)
	second := RenderMarkdown(results)
	if !bytes.Equal([]byte(first), []byte(second)) {
		t.Fatal("two renderings of the same results differ")
	}
}

// TestRenderJSONGolden pins the JSON structure: stable field names,
// stable order, nanosecond durations — the raw-numbers form two runs
// diff cleanly against, with no mean in sight.
func TestRenderJSONGolden(t *testing.T) {
	got, err := RenderJSON(goldenResults())
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if string(got) != wantJSON {
		t.Fatalf("RenderJSON =\n%s\nwant\n%s", got, wantJSON)
	}
}

// TestRenderJSONDeterministic pins the reproducibility contract for
// the raw form: same input, identical bytes, twice.
func TestRenderJSONDeterministic(t *testing.T) {
	results := goldenResults()
	first, err := RenderJSON(results)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	second, err := RenderJSON(results)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two JSON renderings of the same results differ")
	}
}

// TestRenderEmpty pins the empty edge: no results still render a valid
// document — a header-only table and an empty results array — so a
// caller never special-cases the zero input.
func TestRenderEmpty(t *testing.T) {
	wantEmptyMarkdown := `# Bench report

| driver | workload | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
`
	if got := RenderMarkdown(nil); got != wantEmptyMarkdown {
		t.Fatalf("RenderMarkdown(nil) =\n%q\nwant\n%q", got, wantEmptyMarkdown)
	}

	got, err := RenderJSON(nil)
	if err != nil {
		t.Fatalf("RenderJSON(nil): %v", err)
	}
	if want := "{\n  \"results\": []\n}\n"; string(got) != want {
		t.Fatalf("RenderJSON(nil) = %q, want %q", got, want)
	}
}

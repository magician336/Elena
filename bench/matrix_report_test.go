package bench

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// goldenMatrix is one fixed matrix result with hand-set numbers and
// declarations: the golden pins below spell out its exact rendering, so
// every byte — declaration sections, column order, duration and float
// formatting — is deliberate.
func goldenMatrix() MatrixResult {
	return MatrixResult{
		Alignment: []EngineAlignment{
			{
				Engine: "elena",
				Tiers: []TierDeclaration{
					{
						Tier:          SyncPerWrite,
						Aligned:       true,
						Configuration: "every append fdatasync'ed to the data file before it acknowledges (elena.WithSyncAlways, the store default)",
					},
					{
						Tier:    SyncGroupCommit,
						Aligned: true,
						Configuration: "appends acknowledge after the write; a background flusher fdatasyncs the active data file once per 10ms " +
							"(elena.WithSyncInterval; the final flush is forced at close and at every rotation)",
					},
				},
			},
			{
				Engine: "bbolt",
				Tiers: []TierDeclaration{
					{
						Tier:          SyncPerWrite,
						Aligned:       true,
						Configuration: "every append commits as its own write transaction under bbolt's default sync-on-commit (Options.NoSync left false)",
					},
					{
						Tier:    SyncGroupCommit,
						Aligned: false,
						Configuration: "bbolt exposes no periodic-sync configuration " +
							"(NoSync only turns syncing off), so this tier produces no bbolt numbers",
					},
				},
			},
		},
		Cells: []CellResult{
			{
				Cell: Cell{Engine: "elena", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 64},
				Result: Result{
					Driver:   "elena",
					Workload: "ycsb-a",
					Phases: []PhaseResult{
						{
							Phase: "load", Ops: 8, Elapsed: 8 * time.Millisecond, Throughput: 1000,
							Latency: LatencySummary{Count: 8, Min: 400 * time.Microsecond, Max: 900 * time.Microsecond, P50: 500 * time.Microsecond, P99: 900 * time.Microsecond, P999: 900 * time.Microsecond},
						},
						{
							Phase: "run", Ops: 4, Elapsed: 2 * time.Millisecond, Throughput: 2000,
							Latency: LatencySummary{Count: 4, Min: 400 * time.Microsecond, Max: 600 * time.Microsecond, P50: 400 * time.Microsecond, P99: 600 * time.Microsecond, P999: 600 * time.Microsecond},
						},
					},
				},
			},
			{
				Cell: Cell{Engine: "bbolt", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 64},
				Result: Result{
					Driver:   "bbolt",
					Workload: "ycsb-a",
					Phases: []PhaseResult{
						{
							Phase: "load", Ops: 8, Elapsed: 16 * time.Millisecond, Throughput: 500,
							Latency: LatencySummary{Count: 8, Min: 1 * time.Millisecond, Max: 3 * time.Millisecond, P50: 2 * time.Millisecond, P99: 3 * time.Millisecond, P999: 3 * time.Millisecond},
						},
						{
							Phase: "run", Ops: 4, Elapsed: 8 * time.Millisecond, Throughput: 500,
							Latency: LatencySummary{Count: 4, Min: 1 * time.Millisecond, Max: 2 * time.Millisecond, P50: 2 * time.Millisecond, P99: 2 * time.Millisecond, P999: 2 * time.Millisecond},
						},
					},
				},
			},
		},
		Failures: []CellFailure{
			{Cell: Cell{Engine: "elena", Tier: SyncGroupCommit, Mix: YCSBA, ValueSize: 64}, Err: errors.New("open elena in /x: directory locked")},
		},
	}
}

// wantMatrixMarkdown is the golden markdown of goldenMatrix.
const wantMatrixMarkdown = `# YCSB matrix report

## Declarations

workload: one seed declares every workload: each engine and tier drives the identical load-then-run operation sequence the seeded generator emits, so driver differences never leak into workload semantics

elena mapping: one key is one trajectory (namespace bench, session traj-<key>); an update appends the trajectory's next event; a read is the single-event read of the latest acknowledged event. The elena data set grows monotonically — an update is a new event, never an in-place overwrite — while the KV engines overwrite in place; that difference is elena's append-only semantics, declared here rather than hidden

durability, engine by engine:

- elena
  - per-write-sync (aligned): every append fdatasync'ed to the data file before it acknowledges (elena.WithSyncAlways, the store default)
  - group-commit (aligned): appends acknowledge after the write; a background flusher fdatasyncs the active data file once per 10ms (elena.WithSyncInterval; the final flush is forced at close and at every rotation)
- bbolt
  - per-write-sync (aligned): every append commits as its own write transaction under bbolt's default sync-on-commit (Options.NoSync left false)
  - group-commit (not aligned): bbolt exposes no periodic-sync configuration (NoSync only turns syncing off), so this tier produces no bbolt numbers

## Results

| engine | sync tier | mix | value | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| elena | per-write-sync | ycsb-a | 64B | load | 8 | 8ms | 1000.0 | 500µs | 900µs | 900µs | 400µs | 900µs |
| elena | per-write-sync | ycsb-a | 64B | run | 4 | 2ms | 2000.0 | 400µs | 600µs | 600µs | 400µs | 600µs |
| bbolt | per-write-sync | ycsb-a | 64B | load | 8 | 16ms | 500.0 | 2ms | 3ms | 3ms | 1ms | 3ms |
| bbolt | per-write-sync | ycsb-a | 64B | run | 4 | 8ms | 500.0 | 2ms | 2ms | 2ms | 1ms | 2ms |

## Failures

- elena-group-commit-ycsb-a-64B: open elena in /x: directory locked
`

// wantMatrixJSON is the golden JSON of goldenMatrix: the declaration
// document the numbers travel with — workload semantics, the elena
// mapping, the engine-by-engine durability statements — and one result
// object per produced cell whose phases carry the same field names the
// raw single-run document uses.
const wantMatrixJSON = `{
  "declaration": {
    "workload": "one seed declares every workload: each engine and tier drives the identical load-then-run operation sequence the seeded generator emits, so driver differences never leak into workload semantics",
    "elena_mapping": "one key is one trajectory (namespace bench, session traj-\u003ckey\u003e); an update appends the trajectory's next event; a read is the single-event read of the latest acknowledged event. The elena data set grows monotonically — an update is a new event, never an in-place overwrite — while the KV engines overwrite in place; that difference is elena's append-only semantics, declared here rather than hidden",
    "sync": [
      {
        "engine": "elena",
        "tiers": [
          {
            "tier": "per-write-sync",
            "aligned": true,
            "configuration": "every append fdatasync'ed to the data file before it acknowledges (elena.WithSyncAlways, the store default)"
          },
          {
            "tier": "group-commit",
            "aligned": true,
            "configuration": "appends acknowledge after the write; a background flusher fdatasyncs the active data file once per 10ms (elena.WithSyncInterval; the final flush is forced at close and at every rotation)"
          }
        ]
      },
      {
        "engine": "bbolt",
        "tiers": [
          {
            "tier": "per-write-sync",
            "aligned": true,
            "configuration": "every append commits as its own write transaction under bbolt's default sync-on-commit (Options.NoSync left false)"
          },
          {
            "tier": "group-commit",
            "aligned": false,
            "configuration": "bbolt exposes no periodic-sync configuration (NoSync only turns syncing off), so this tier produces no bbolt numbers"
          }
        ]
      }
    ]
  },
  "results": [
    {
      "engine": "elena",
      "sync_tier": "per-write-sync",
      "mix": "ycsb-a",
      "value_size": 64,
      "phases": [
        {
          "phase": "load",
          "ops": 8,
          "elapsed_ns": 8000000,
          "throughput_ops_sec": 1000,
          "latency": {
            "count": 8,
            "min_ns": 400000,
            "p50_ns": 500000,
            "p99_ns": 900000,
            "p999_ns": 900000,
            "max_ns": 900000
          }
        },
        {
          "phase": "run",
          "ops": 4,
          "elapsed_ns": 2000000,
          "throughput_ops_sec": 2000,
          "latency": {
            "count": 4,
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
      "engine": "bbolt",
      "sync_tier": "per-write-sync",
      "mix": "ycsb-a",
      "value_size": 64,
      "phases": [
        {
          "phase": "load",
          "ops": 8,
          "elapsed_ns": 16000000,
          "throughput_ops_sec": 500,
          "latency": {
            "count": 8,
            "min_ns": 1000000,
            "p50_ns": 2000000,
            "p99_ns": 3000000,
            "p999_ns": 3000000,
            "max_ns": 3000000
          }
        },
        {
          "phase": "run",
          "ops": 4,
          "elapsed_ns": 8000000,
          "throughput_ops_sec": 500,
          "latency": {
            "count": 4,
            "min_ns": 1000000,
            "p50_ns": 2000000,
            "p99_ns": 2000000,
            "p999_ns": 2000000,
            "max_ns": 2000000
          }
        }
      ]
    }
  ],
  "failures": [
    {
      "engine": "elena",
      "sync_tier": "group-commit",
      "mix": "ycsb-a",
      "value_size": 64,
      "error": "open elena in /x: directory locked"
    }
  ]
}
`

// TestRenderMatrixMarkdownGolden pins the markdown rendering byte for
// byte: the declarations ahead of the numbers, one table row per phase,
// latency as the percentile trio, failures listed by cell.
func TestRenderMatrixMarkdownGolden(t *testing.T) {
	if got := RenderMatrixMarkdown(goldenMatrix()); got != wantMatrixMarkdown {
		t.Fatalf("RenderMatrixMarkdown =\n%q\nwant\n%q", got, wantMatrixMarkdown)
	}
}

// TestRenderMatrixJSONGolden pins the raw document: fixed field names
// in fixed order, the durability and mapping declarations beside the
// numbers, and phase fields identical to the single-run raw document's.
func TestRenderMatrixJSONGolden(t *testing.T) {
	got, err := RenderMatrixJSON(goldenMatrix())
	if err != nil {
		t.Fatalf("RenderMatrixJSON: %v", err)
	}
	if string(got) != wantMatrixJSON {
		t.Fatalf("RenderMatrixJSON =\n%s\nwant\n%s", got, wantMatrixJSON)
	}
}

// TestRenderMatrixDeterministic pins the reproducibility contract: the
// same matrix result renders to identical bytes twice — no clocks, no
// map iteration, no environment leaks into the report.
func TestRenderMatrixDeterministic(t *testing.T) {
	res := goldenMatrix()
	mdFirst := RenderMatrixMarkdown(res)
	mdSecond := RenderMatrixMarkdown(res)
	if mdFirst != mdSecond {
		t.Fatal("two markdown renderings of the same result differ")
	}
	first, err := RenderMatrixJSON(res)
	if err != nil {
		t.Fatalf("RenderMatrixJSON: %v", err)
	}
	second, err := RenderMatrixJSON(res)
	if err != nil {
		t.Fatalf("RenderMatrixJSON again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two JSON renderings of the same result differ")
	}
}

// TestRenderMatrixEmpty pins the empty edge: no cells and no failures
// still render a valid document — declarations plus an empty results
// array and no failures section — so a caller never special-cases the
// zero input.
func TestRenderMatrixEmpty(t *testing.T) {
	res := MatrixResult{
		Alignment: []EngineAlignment{{Engine: "elena", Tiers: []TierDeclaration{{Tier: SyncPerWrite, Aligned: true, Configuration: "cfg"}}}},
	}
	md := RenderMatrixMarkdown(res)
	if !bytes.Contains([]byte(md), []byte("| --- |")) || bytes.Contains([]byte(md), []byte("## Failures")) {
		t.Fatalf("empty markdown keeps the table and drops the failures section:\n%s", md)
	}
	got, err := RenderMatrixJSON(res)
	if err != nil {
		t.Fatalf("RenderMatrixJSON: %v", err)
	}
	if !bytes.Contains(got, []byte(`"results": []`)) || !bytes.Contains(got, []byte(`"failures": []`)) {
		t.Fatalf("empty JSON carries empty arrays:\n%s", got)
	}
}

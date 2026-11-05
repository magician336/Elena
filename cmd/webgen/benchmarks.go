package main

// The third page act distills a small, precise set of numbers from the
// raw benchmark JSON documents checked in under
// docs/benchmarks/data — the same files the benchmark report cites.
// Every figure on the page is computed here from those files; nothing
// is typed in by hand, so the page can never drift from the evidence.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// EngineFigure is one engine's run-phase figure on one workload cell.
type EngineFigure struct {
	Name  string  `json:"name"`
	OpsSec float64 `json:"ops_sec"`
	P99Ms float64 `json:"p99_ms"`
}

// SameTierWrite is the same-tier comparison anchor: every engine on
// one cell of the per-write-synchronized tier.
type SameTierWrite struct {
	Mix        string          `json:"mix"`
	Tier       string          `json:"tier"`
	ValueBytes int             `json:"value_bytes"`
	Engines    []EngineFigure  `json:"engines"`
}

// ReadOnlyLoss is the honest loss: read-only throughput where the
// structure decides, elena against bbolt.
type ReadOnlyLoss struct {
	Mix         string  `json:"mix"`
	ValueBytes  int     `json:"value_bytes"`
	ElenaOpsSec float64 `json:"elena_ops_sec"`
	BboltOpsSec float64 `json:"bbolt_ops_sec"`
}

// GroupCommitJump is one tier step on one cell: per-write sync
// against the 10 ms group-commit period.
type GroupCommitJump struct {
	Mix               string  `json:"mix"`
	ValueBytes        int     `json:"value_bytes"`
	PerWriteOpsSec    float64 `json:"per_write_ops_sec"`
	GroupCommitOpsSec float64 `json:"group_commit_ops_sec"`
}

// MergeWindow is the merge-under-load claim: the merge's wall time
// and the min/max append P50 across every observation window.
type MergeWindow struct {
	WallMs           float64 `json:"wall_ms"`
	P50LowMs         float64 `json:"p50_low_ms"`
	P50HighMs        float64 `json:"p50_high_ms"`
	Windows          int     `json:"windows"`
	AppendsObserved  int     `json:"appends_observed"`
	ReadsObserved    int     `json:"reads_observed"`
}

// StartupPairView is the paired startup timing: the same dataset
// opened with hint files in place and with them removed.
type StartupPairView struct {
	HintOpenMs   float64 `json:"hint_open_ms"`
	NoHintOpenMs float64 `json:"no_hint_open_ms"`
	LiveEntries  int     `json:"live_entries"`
	DataMB       float64 `json:"data_mb"`
}

// WriteAmpView is the write-amplification pair around one merge.
type WriteAmpView struct {
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// SingleWriterCurve is the throughput cost of the second concurrent
// appender — reported as the honest structural fact it is.
type SingleWriterCurve struct {
	OneWorkerOpsSec  float64 `json:"one_worker_ops_sec"`
	TwoWorkersOpsSec float64 `json:"two_workers_ops_sec"`
	Ratio            float64 `json:"ratio"`
}

// LongRunView is the write-synchronized steady state over a grown
// store.
type LongRunView struct {
	Ops     int     `json:"ops"`
	Minutes float64 `json:"minutes"`
	OpsSec  float64 `json:"ops_sec"`
}

// Benchmarks is the page's whole third act.
type Benchmarks struct {
	SameTierWrite SameTierWrite     `json:"same_tier_write"`
	ReadOnlyLoss  ReadOnlyLoss      `json:"read_only_loss"`
	GroupCommit   GroupCommitJump   `json:"group_commit"`
	MergeWindow   MergeWindow       `json:"merge_window"`
	StartupPair   StartupPairView   `json:"startup_pair"`
	WriteAmp      WriteAmpView      `json:"write_amplification"`
	SingleWriter  SingleWriterCurve `json:"single_writer"`
	LongRun       LongRunView       `json:"long_run"`
}

// quantizeOpsSec rounds a throughput to the register the benchmark
// report reads in: whole ops/sec from a thousand up, one decimal
// below.
func quantizeOpsSec(x float64) float64 {
	if x >= 1000 {
		return math.Round(x)
	}
	return math.Round(x*10) / 10
}

// roundMs rounds nanoseconds to a tenth of a millisecond.
func roundMs(ns float64) float64 {
	return math.Round(ns/1e6*10) / 10
}

// round2 rounds to two decimals — the ratio and amplification pair's
// register.
func round2(x float64) float64 {
	return math.Round(x*100) / 100
}

// loadJSON reads and decodes one raw document.
func loadJSON(path string, doc any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// The raw documents' shapes, mirrored field by field — only the
// fields the distillation reads.
type matrixDoc struct {
	Results []matrixResult `json:"results"`
}

type matrixResult struct {
	Engine    string        `json:"engine"`
	SyncTier  string        `json:"sync_tier"`
	Mix       string        `json:"mix"`
	ValueSize int           `json:"value_size"`
	Phases    []matrixPhase `json:"phases"`
}

type matrixPhase struct {
	Phase      string         `json:"phase"`
	Throughput float64        `json:"throughput_ops_sec"`
	Latency    matrixLatency  `json:"latency"`
}

type matrixLatency struct {
	P99Ns float64 `json:"p99_ns"`
}

type mergeCurveDoc struct {
	Result struct {
		MergeElapsedNs float64      `json:"merge_elapsed_ns"`
		Points         []mergePoint `json:"points"`
	} `json:"result"`
}

type mergePoint struct {
	Appends     int     `json:"appends"`
	Reads       int     `json:"reads"`
	AppendP50Ns float64 `json:"append_p50_ns"`
}

type startupPairDoc struct {
	Result struct {
		Hint struct {
			OpenNs     float64 `json:"open_ns"`
			LiveEntries int    `json:"live_entries"`
			DataBytes  float64 `json:"data_bytes"`
		} `json:"hint"`
		NoHint struct {
			OpenNs float64 `json:"open_ns"`
		} `json:"no_hint"`
	} `json:"result"`
}

type writeAmpDoc struct {
	Result struct {
		BeforeMerge struct {
			Ratio float64 `json:"ratio"`
		} `json:"before_merge"`
		AfterMerge struct {
			Ratio float64 `json:"ratio"`
		} `json:"after_merge"`
	} `json:"result"`
}

type curveDoc struct {
	Points []struct {
		Concurrency int     `json:"concurrency"`
		Throughput  float64 `json:"throughput_ops_sec"`
	} `json:"points"`
}

type scenarioDoc struct {
	Results []struct {
		Phases []struct {
			Ops        int     `json:"ops"`
			ElapsedNs  float64 `json:"elapsed_ns"`
			Throughput float64 `json:"throughput_ops_sec"`
		} `json:"phases"`
	} `json:"results"`
}

// findRun locates one engine's run phase on one matrix cell and
// returns its throughput and P99.
func findRun(results []matrixResult, engine, tier, mix string, value int) (throughput, p99ns float64, err error) {
	for _, res := range results {
		if res.Engine != engine || res.SyncTier != tier || res.Mix != mix || res.ValueSize != value {
			continue
		}
		for _, ph := range res.Phases {
			if ph.Phase == "run" {
				return ph.Throughput, ph.Latency.P99Ns, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("matrix cell not found: %s %s %s value %d", engine, tier, mix, value)
}

// sameTierWrite collects the per-write tier's figures for every
// engine, in the report's engine order; a missing engine is an error —
// a broken grid must fail the generation, not silently shrink.
func sameTierWrite(doc matrixDoc) (SameTierWrite, error) {
	const (
		tier  = "per-write-sync"
		mix   = "ycsb-a"
		value = 1024
	)
	out := SameTierWrite{Mix: mix, Tier: tier, ValueBytes: value}
	for _, engine := range []string{"elena", "bbolt", "badger", "rosedb"} {
		ops, p99ns, err := findRun(doc.Results, engine, tier, mix, value)
		if err != nil {
			return SameTierWrite{}, err
		}
		out.Engines = append(out.Engines, EngineFigure{
			Name: engine, OpsSec: quantizeOpsSec(ops), P99Ms: roundMs(p99ns),
		})
	}
	return out, nil
}

// readOnlyLoss collects the read-only cell the page's honest-loss card
// cites.
func readOnlyLoss(doc matrixDoc) (ReadOnlyLoss, error) {
	const (
		tier  = "per-write-sync"
		mix   = "ycsb-c"
		value = 16384
	)
	elenaOps, _, err := findRun(doc.Results, "elena", tier, mix, value)
	if err != nil {
		return ReadOnlyLoss{}, err
	}
	bboltOps, _, err := findRun(doc.Results, "bbolt", tier, mix, value)
	if err != nil {
		return ReadOnlyLoss{}, err
	}
	return ReadOnlyLoss{
		Mix: mix, ValueBytes: value,
		ElenaOpsSec: quantizeOpsSec(elenaOps), BboltOpsSec: quantizeOpsSec(bboltOps),
	}, nil
}

// groupCommitJump reads the tier step on the 64 B A-mix cell.
func groupCommitJump(doc matrixDoc) (GroupCommitJump, error) {
	const (
		mix   = "ycsb-a"
		value = 64
	)
	perWrite, _, err := findRun(doc.Results, "elena", "per-write-sync", mix, value)
	if err != nil {
		return GroupCommitJump{}, err
	}
	grouped, _, err := findRun(doc.Results, "elena", "group-commit", mix, value)
	if err != nil {
		return GroupCommitJump{}, err
	}
	return GroupCommitJump{
		Mix: mix, ValueBytes: value,
		PerWriteOpsSec: quantizeOpsSec(perWrite), GroupCommitOpsSec: quantizeOpsSec(grouped),
	}, nil
}

// mergeWindow reduces the merge-curve windows to the page's claim:
// the wall time, the min/max append P50 across every window, and how
// much load the curve observed.
func mergeWindow(doc mergeCurveDoc) MergeWindow {
	out := MergeWindow{WallMs: roundMs(doc.Result.MergeElapsedNs), Windows: len(doc.Result.Points)}
	for _, p := range doc.Result.Points {
		out.AppendsObserved += p.Appends
		out.ReadsObserved += p.Reads
		p50 := roundMs(p.AppendP50Ns)
		if out.P50LowMs == 0 || p50 < out.P50LowMs {
			out.P50LowMs = p50
		}
		if p50 > out.P50HighMs {
			out.P50HighMs = p50
		}
	}
	return out
}

// curvePoint returns the throughput of one concurrency step.
func curvePoint(doc curveDoc, concurrency int) (float64, error) {
	for _, p := range doc.Points {
		if p.Concurrency == concurrency {
			return p.Throughput, nil
		}
	}
	return 0, fmt.Errorf("curve has no concurrency-%d point", concurrency)
}

// distillBenchmarks reads the checked-in raw documents in dataDir and
// reduces them to the page's number set. Any missing document or
// missing cell is an error: every figure on the page must be
// followable to the raw document it came from.
func distillBenchmarks(dataDir string) (Benchmarks, error) {
	var matrix matrixDoc
	if err := loadJSON(filepath.Join(dataDir, "matrix-grid.json"), &matrix); err != nil {
		return Benchmarks{}, fmt.Errorf("matrix grid: %w", err)
	}
	var mergeDoc mergeCurveDoc
	if err := loadJSON(filepath.Join(dataDir, "focused-merge-curve.json"), &mergeDoc); err != nil {
		return Benchmarks{}, fmt.Errorf("merge curve: %w", err)
	}
	var startup startupPairDoc
	if err := loadJSON(filepath.Join(dataDir, "focused-startup-pair.json"), &startup); err != nil {
		return Benchmarks{}, fmt.Errorf("startup pair: %w", err)
	}
	var amp writeAmpDoc
	if err := loadJSON(filepath.Join(dataDir, "focused-write-amp.json"), &amp); err != nil {
		return Benchmarks{}, fmt.Errorf("write amplification: %w", err)
	}
	var curve curveDoc
	if err := loadJSON(filepath.Join(dataDir, "curve-sync-none.json"), &curve); err != nil {
		return Benchmarks{}, fmt.Errorf("concurrency curve: %w", err)
	}
	var scenario scenarioDoc
	if err := loadJSON(filepath.Join(dataDir, "scenario-append.json"), &scenario); err != nil {
		return Benchmarks{}, fmt.Errorf("append scenario: %w", err)
	}
	if len(scenario.Results) == 0 || len(scenario.Results[0].Phases) == 0 {
		return Benchmarks{}, fmt.Errorf("append scenario: no phases recorded")
	}

	out := Benchmarks{}
	var err error
	if out.SameTierWrite, err = sameTierWrite(matrix); err != nil {
		return Benchmarks{}, err
	}
	if out.ReadOnlyLoss, err = readOnlyLoss(matrix); err != nil {
		return Benchmarks{}, err
	}
	if out.GroupCommit, err = groupCommitJump(matrix); err != nil {
		return Benchmarks{}, err
	}
	out.MergeWindow = mergeWindow(mergeDoc)
	out.StartupPair = StartupPairView{
		HintOpenMs:   roundMs(startup.Result.Hint.OpenNs),
		NoHintOpenMs: roundMs(startup.Result.NoHint.OpenNs),
		LiveEntries:  startup.Result.Hint.LiveEntries,
		DataMB:       math.Round(startup.Result.Hint.DataBytes/1e6*10) / 10,
	}
	out.WriteAmp = WriteAmpView{Before: round2(amp.Result.BeforeMerge.Ratio), After: round2(amp.Result.AfterMerge.Ratio)}

	one, err := curvePoint(curve, 1)
	if err != nil {
		return Benchmarks{}, err
	}
	two, err := curvePoint(curve, 2)
	if err != nil {
		return Benchmarks{}, err
	}
	out.SingleWriter = SingleWriterCurve{
		OneWorkerOpsSec: quantizeOpsSec(one), TwoWorkersOpsSec: quantizeOpsSec(two),
		Ratio: round2(two / one),
	}

	appendPhase := scenario.Results[0].Phases[0]
	out.LongRun = LongRunView{
		Ops:     appendPhase.Ops,
		Minutes: math.Round(appendPhase.ElapsedNs/1e9/60*10) / 10,
		OpsSec:  quantizeOpsSec(appendPhase.Throughput),
	}
	return out, nil
}

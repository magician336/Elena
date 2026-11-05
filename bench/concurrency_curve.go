package bench

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/magician336/elena"
)

// curveNamespace is the one namespace the concurrency curve drives:
// every worker owns one trajectory inside it.
const curveNamespace = "curve"

// syncTierAlways and syncTierNone name the two durability tiers the
// curve runs: the store's default — every append synchronized on
// acknowledgment — and the no-sync contrast tier that removes fsync
// from the write path, so a throughput plateau in one tier can be
// attributed to what the tier still serializes.
const (
	syncTierAlways = "sync-always"
	syncTierNone   = "sync-none"
)

// CurveSpec declares the throughput-concurrency curve: for each step
// in Concurrency, that many workers each append OpsPerWorker events of
// ValueSize bytes to their own trajectory, concurrently, through the
// public elena library. SyncNone selects the no-sync store tier for
// the whole curve — the default tier synchronizes every append. Every
// step value, the ops, and the value size must be positive, and there
// must be at least one step.
type CurveSpec struct {
	Concurrency  []int
	OpsPerWorker int
	ValueSize    int
	SyncNone     bool
}

// CurvePoint is one step of the curve: how many workers ran, how many
// events they appended in total, the step's wall clock, the aggregate
// throughput over the two, and the latency summary over every
// worker's per-event samples.
type CurvePoint struct {
	Concurrency int
	Ops         int
	Elapsed     time.Duration
	Throughput  float64
	Latency     LatencySummary
}

// CurveResult is one curve run: the names for the report, the
// durability tier it ran under, and one point per concurrency step in
// the order the spec declared them.
type CurveResult struct {
	Driver   string
	Workload string
	SyncTier string
	Points   []CurvePoint
}

// RunAppendCurve drives the concurrency curve through the public elena
// library. Each step runs in its own fresh store directory under dir
// ("c<N>"), so every step starts from the same empty-store state and
// step-to-step comparison is not confounded by data accumulated by the
// earlier steps. Inside a step the worker sessions are created up
// front, then every worker appends its events concurrently from a
// shared start signal; the step's wall clock spans that signal to the
// last worker's finish, so session creation and store open and close
// stay out of the measured window. Each append verifies the sequence
// number its trajectory acknowledged, so a lost or reordered event
// fails the curve instead of silently vanishing from the totals.
func RunAppendCurve(dir string, spec CurveSpec) (CurveResult, error) {
	if len(spec.Concurrency) == 0 {
		return CurveResult{}, fmt.Errorf("concurrency curve: no concurrency steps declared")
	}
	for i, c := range spec.Concurrency {
		if c <= 0 {
			return CurveResult{}, fmt.Errorf("concurrency curve: step %d is %d, must be positive", i, c)
		}
	}
	if spec.OpsPerWorker <= 0 {
		return CurveResult{}, fmt.Errorf("concurrency curve: ops per worker %d must be positive", spec.OpsPerWorker)
	}
	if spec.ValueSize <= 0 {
		return CurveResult{}, fmt.Errorf("concurrency curve: value size %d must be positive", spec.ValueSize)
	}
	tier := syncTierAlways
	opts := []elena.OpenOption{elena.WithSweepDisabled()}
	if spec.SyncNone {
		tier = syncTierNone
		opts = append(opts, elena.WithSyncNone())
	}

	res := CurveResult{Driver: "elena", Workload: "elena-concurrency-curve", SyncTier: tier}
	for _, workers := range spec.Concurrency {
		point, err := runCurveStep(dir, workers, spec, opts)
		if err != nil {
			return CurveResult{}, err
		}
		res.Points = append(res.Points, point)
	}
	return res, nil
}

// runCurveStep drives one concurrency step in its own store directory
// and returns its point.
func runCurveStep(dir string, workers int, spec CurveSpec, opts []elena.OpenOption) (CurvePoint, error) {
	stepDir := filepath.Join(dir, fmt.Sprintf("c%d", workers))
	store, err := elena.Open(stepDir, opts...)
	if err != nil {
		return CurvePoint{}, fmt.Errorf("concurrency curve: open %s: %w", stepDir, err)
	}
	sessions := make([]*elena.Session, workers)
	for w := range sessions {
		sess, err := store.CreateSession(curveNamespace, curveWorkerName(w))
		if err != nil {
			return CurvePoint{}, fmt.Errorf("concurrency curve: create worker %d: %w", w, err)
		}
		sessions[w] = sess
	}

	lats := make([]Latency, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range sessions {
		go func(w int, sess *elena.Session) {
			defer wg.Done()
			<-start
			for e := 0; e < spec.OpsPerWorker; e++ {
				opStart := time.Now()
				seq, err := sess.Append(genValue(uint64(w), e, spec.ValueSize))
				lats[w].Observe(time.Since(opStart))
				if err != nil {
					errs[w] = fmt.Errorf("append worker %d event %d: %w", w, e, err)
					return
				}
				if seq != uint64(e)+1 {
					errs[w] = fmt.Errorf("append worker %d event %d acknowledged seq %d, want %d", w, e, seq, e+1)
					return
				}
			}
		}(w, sessions[w])
	}
	begin := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)
	if err := errors.Join(errs...); err != nil {
		return CurvePoint{}, fmt.Errorf("concurrency curve: %d workers: %w (close: %v)", workers, err, store.Close())
	}
	if err := store.Close(); err != nil {
		return CurvePoint{}, fmt.Errorf("concurrency curve: close %s: %w", stepDir, err)
	}

	merged := mergeLatencies(lats)
	ops := workers * spec.OpsPerWorker
	return CurvePoint{
		Concurrency: workers,
		Ops:         ops,
		Elapsed:     elapsed,
		Throughput:  Throughput(ops, elapsed),
		Latency:     merged.Summarize(),
	}, nil
}

// mergeLatencies concatenates the workers' collectors into one, in
// worker order — the merged observation order is deterministic, so
// summaries of the same run are reproducible.
func mergeLatencies(lats []Latency) Latency {
	var merged Latency
	for _, l := range lats {
		merged.samples = append(merged.samples, l.samples...)
	}
	return merged
}

// curveWorkerName renders worker w's trajectory name.
func curveWorkerName(w int) string {
	return fmt.Sprintf("worker-%d", w)
}

// RenderCurveMarkdown renders one curve as the human report: a header
// naming the driver and durability tier, then one row per concurrency
// step — its worker count, event total, wall clock, aggregate
// throughput, the throughput ratio against the first step, and the
// latency trio. The ratio column is the lock-contention reading: a
// curve that plateaus or falls shows throughput not scaling with
// concurrency. Rendering is a pure function of its input: same curve,
// same bytes, every time.
func RenderCurveMarkdown(curve CurveResult) string {
	var b strings.Builder
	b.WriteString("# Concurrency curve\n\n")
	fmt.Fprintf(&b, "driver %s, workload %s, durability %s\n\n", curve.Driver, curve.Workload, curve.SyncTier)
	b.WriteString("| concurrency | ops | elapsed | ops/sec | x-step1 | p50 | p99 | p999 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	base := 0.0
	if len(curve.Points) > 0 {
		base = curve.Points[0].Throughput
	}
	for _, pt := range curve.Points {
		ratio := "-"
		if base > 0 {
			ratio = fmt.Sprintf("%.2fx", pt.Throughput/base)
		}
		lat := pt.Latency
		fmt.Fprintf(&b, "| %d | %d | %s | %.1f | %s | %s | %s | %s |\n",
			pt.Concurrency, pt.Ops, pt.Elapsed.Round(reportPrecision), pt.Throughput, ratio,
			lat.P50.Round(reportPrecision), lat.P99.Round(reportPrecision), lat.P999.Round(reportPrecision))
	}
	return b.String()
}

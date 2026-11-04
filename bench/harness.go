package bench

import (
	"errors"
	"fmt"
	"time"
)

// PhaseResult carries one phase's numbers: how many operations ran, how
// long the phase took wall-clock, the throughput derived from the two,
// and the latency summary over the phase's per-operation samples.
type PhaseResult struct {
	Phase      string
	Ops        int
	Elapsed    time.Duration
	Throughput float64
	Latency    LatencySummary
}

// Result is one workload run against one driver: the names for the
// report plus one PhaseResult per workload phase, in workload order.
type Result struct {
	Driver   string
	Workload string
	Phases   []PhaseResult
}

// Run drives d through w under dir: it opens the driver, times each
// phase as a whole while observing every single operation's latency,
// and closes the driver. The dir must exist; the driver decides what
// appears inside it. A driver opened by Run is closed by Run even when
// an operation fails — only a failed Open leaves Close uncalled. On any
// failure the partial Result is discarded and the errors joined, an
// operation failure ahead of a close failure.
func Run(d Driver, dir string, w Workload) (Result, error) {
	if err := d.Open(dir); err != nil {
		return Result{}, fmt.Errorf("open %s in %s: %w", d.Name(), dir, err)
	}
	res, runErr := runPhases(d, w)
	closeErr := d.Close()
	return res, errors.Join(runErr, closeErr)
}

// runPhases drives every phase in workload order and collects the
// per-phase numbers; a mid-run failure discards the partial result.
func runPhases(d Driver, w Workload) (Result, error) {
	res := Result{Driver: d.Name(), Workload: w.Name, Phases: make([]PhaseResult, 0, len(w.Phases))}
	for _, phase := range w.Phases {
		pr, err := runPhase(d, phase)
		if err != nil {
			return Result{}, err
		}
		res.Phases = append(res.Phases, pr)
	}
	return res, nil
}

// runPhase times one phase wall-clock, observes each operation's
// latency, and derives the phase's throughput from the two.
func runPhase(d Driver, phase Phase) (PhaseResult, error) {
	var lat Latency
	phaseStart := time.Now()
	for _, op := range phase.Ops {
		opStart := time.Now()
		if err := d.Do(op); err != nil {
			return PhaseResult{}, fmt.Errorf("%s %d: %w", op.Kind, op.Key, err)
		}
		lat.Observe(time.Since(opStart))
	}
	elapsed := time.Since(phaseStart)
	return PhaseResult{
		Phase:      phase.Name,
		Ops:        len(phase.Ops),
		Elapsed:    elapsed,
		Throughput: Throughput(len(phase.Ops), elapsed),
		Latency:    lat.Summarize(),
	}, nil
}

package bench

import (
	"fmt"
	"os"
	"time"

	"elena"
)

// MergeCurveConfig declares the merge-curve scenario's shape. A mixed
// append+read load over Sessions trajectories of ValueSize-byte events
// runs until WarmupOps operations have been issued; one explicit
// public Merge then runs while the load continues, and per-operation
// latencies are bucketed into fixed windows of Window —
// ContextWindows before the merge start, the merge's own duration
// rounded up to whole windows, and TailWindows past the merge end.
// Sessions, ValueSize, WarmupOps, Window, and TailWindows must be
// positive; ContextWindows may be zero.
type MergeCurveConfig struct {
	Sessions       int
	ValueSize      int
	WarmupOps      int
	Window         time.Duration
	ContextWindows int
	TailWindows    int
}

// MergeCurvePoint is one window of the curve: the window's start
// offset relative to the merge start (negative before it), the
// operation counts the window caught, and each kind's P50/P99 within
// the window. A window that caught no operations of a kind carries
// zero counts and zero percentiles for it — absence, not a zero that
// reads like a measurement.
type MergeCurvePoint struct {
	OffsetNS    int64 `json:"offset_ns"`
	Appends     int   `json:"appends"`
	Reads       int   `json:"reads"`
	AppendP50NS int64 `json:"append_p50_ns"`
	AppendP99NS int64 `json:"append_p99_ns"`
	ReadP50NS   int64 `json:"read_p50_ns"`
	ReadP99NS   int64 `json:"read_p99_ns"`
}

// MergeCurveResult is the curve evidence: the merge's own wall time,
// the window width every offset is stepped in, the point sequence, and
// the total operation counts the load issued (samples outside the
// sampled range included in these totals, dropped from the windows).
type MergeCurveResult struct {
	MergeElapsedNS  int64             `json:"merge_elapsed_ns"`
	WindowNS        int64             `json:"window_ns"`
	Points          []MergeCurvePoint `json:"points"`
	ObservedAppends int               `json:"observed_appends"`
	ObservedReads   int               `json:"observed_reads"`
}

// mergeSample is one operation the load observed, already reduced to
// its curve coordinates: the offset of the operation's start from the
// merge start (negative before it) and the operation's latency.
type mergeSample struct {
	kind   OpKind
	offset time.Duration
	lat    time.Duration
}

// rawSample is one observation before the merge start is known: the
// operation's start instant, from which the offset is derived once the
// merge has been triggered.
type rawSample struct {
	kind  OpKind
	start time.Time
	lat   time.Duration
}

// loadOutcome is what the load goroutine reports back: its samples in
// issue order, or the error that stopped it early.
type loadOutcome struct {
	samples []rawSample
	err     error
}

// RunMergeCurve drives the merge-curve scenario: a mixed append+read
// load over a fresh store under dir — which must not exist; the store
// creates it — reaches its declared warmup, one explicit public Merge
// runs while the load keeps going, and the tail windows elapse before
// the load stops. The result is the latency-over-time curve across the
// merge window: the evidence that a merge is a period of visible but
// bounded latency, not a stop. Every read verifies the newest
// acknowledged event of its trajectory, so a read path that degrades
// into wrong answers fails the run instead of drawing a curve.
func RunMergeCurve(dir string, cfg MergeCurveConfig) (MergeCurveResult, error) {
	if cfg.Sessions <= 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: sessions %d must be positive", cfg.Sessions)
	}
	if cfg.ValueSize <= 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: value size %d must be positive", cfg.ValueSize)
	}
	if cfg.WarmupOps <= 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: warmup ops %d must be positive", cfg.WarmupOps)
	}
	if cfg.Window <= 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: window %s must be positive", cfg.Window)
	}
	if cfg.ContextWindows < 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: context windows %d must not be negative", cfg.ContextWindows)
	}
	if cfg.TailWindows <= 0 {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: tail windows %d must be positive", cfg.TailWindows)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: create %s: %w", dir, err)
	}

	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return MergeCurveResult{}, err
	}
	res, runErr := runMergeCurve(store, cfg)
	closeErr := store.Close()
	if runErr != nil {
		return MergeCurveResult{}, runErr
	}
	if closeErr != nil {
		return MergeCurveResult{}, closeErr
	}
	return res, nil
}

// runMergeCurve performs the scenario on the open store; the caller
// owns the store's lifecycle.
func runMergeCurve(store *elena.Store, cfg MergeCurveConfig) (MergeCurveResult, error) {
	seed := &curveSeed{latest: make([]lastEvent, cfg.Sessions)}
	sessions, err := seed.prime(store, cfg)
	if err != nil {
		return MergeCurveResult{}, err
	}

	warm := make(chan struct{})
	stop := make(chan struct{})
	outcome := make(chan loadOutcome, 1)
	go driveCurveLoad(sessions, seed, cfg, warm, stop, outcome)

	var load loadOutcome
	select {
	case <-warm:
	case load = <-outcome:
		close(stop)
		return MergeCurveResult{}, load.err
	}

	mergeStart := time.Now()
	mergeErr := store.Merge()
	mergeElapsed := time.Since(mergeStart)
	time.Sleep(time.Duration(cfg.TailWindows) * cfg.Window)
	close(stop)
	load = <-outcome
	if load.err != nil {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: load: %w", load.err)
	}
	if mergeErr != nil {
		return MergeCurveResult{}, fmt.Errorf("merge-curve scenario: merge: %w", mergeErr)
	}

	samples := make([]mergeSample, len(load.samples))
	appends, reads := 0, 0
	for i, raw := range load.samples {
		samples[i] = mergeSample{kind: raw.kind, offset: raw.start.Sub(mergeStart), lat: raw.lat}
		if raw.kind == OpAppend {
			appends++
		} else {
			reads++
		}
	}
	from := -time.Duration(cfg.ContextWindows) * cfg.Window
	to := roundUpWindow(mergeElapsed, cfg.Window) + time.Duration(cfg.TailWindows)*cfg.Window
	return MergeCurveResult{
		MergeElapsedNS:  int64(mergeElapsed),
		WindowNS:        int64(cfg.Window),
		Points:          bucketMergeSamples(samples, cfg.Window, from, to),
		ObservedAppends: appends,
		ObservedReads:   reads,
	}, nil
}

// curveSeed is the load's own bookkeeping: per trajectory, what the
// last append acknowledged. One goroutine owns it, so no lock guards
// it; the merge runs concurrently without touching it.
type curveSeed struct {
	latest []lastEvent
}

// prime creates every trajectory and appends its first event, so the
// load's reads always have a target; returns the session handles the
// load drives.
func (s *curveSeed) prime(store *elena.Store, cfg MergeCurveConfig) ([]*elena.Session, error) {
	sessions := make([]*elena.Session, cfg.Sessions)
	for key := 0; key < cfg.Sessions; key++ {
		sess, err := store.CreateSession(elenaNamespace, elenaSessionName(uint64(key)))
		if err != nil {
			return nil, err
		}
		value := genValue(uint64(key), 0, cfg.ValueSize)
		seq, err := sess.Append(value)
		if err != nil {
			return nil, fmt.Errorf("merge-curve scenario: prime key %d: %w", key, err)
		}
		sessions[key] = sess
		s.latest[key] = lastEvent{seq: seq, payload: len(value)}
	}
	return sessions, nil
}

// driveCurveLoad issues the mixed load — appends and reads alternating
// over the trajectories in key order — until stop closes, announcing
// warmup by closing warm once WarmupOps operations have been issued.
// It owns its samples and its trajectory bookkeeping; the single
// outcome send never blocks (the channel is buffered).
func driveCurveLoad(sessions []*elena.Session, seed *curveSeed, cfg MergeCurveConfig, warm, stop chan struct{}, outcome chan<- loadOutcome) {
	ordinals := make([]int, len(sessions))
	var samples []rawSample
	issued := 0
	warmAnnounced := false
	fail := func(err error) {
		outcome <- loadOutcome{samples: samples, err: err}
	}
	for i := 0; ; i++ {
		select {
		case <-stop:
			fail(nil)
			return
		default:
		}
		key := uint64(i/2) % uint64(len(sessions))
		sess := sessions[key]
		if i%2 == 0 {
			value := genValue(key, ordinals[key], cfg.ValueSize)
			start := time.Now()
			seq, err := sess.Append(value)
			lat := time.Since(start)
			if err != nil {
				fail(fmt.Errorf("append key %d: %w", key, err))
				return
			}
			ordinals[key]++
			seed.latest[key] = lastEvent{seq: seq, payload: len(value)}
			samples = append(samples, rawSample{kind: OpAppend, start: start, lat: lat})
		} else {
			want := seed.latest[key]
			start := time.Now()
			event, err := sess.Event(want.seq)
			lat := time.Since(start)
			if err != nil {
				fail(fmt.Errorf("read key %d: %w", key, err))
				return
			}
			if event.Seq != want.seq || len(event.Payload) != want.payload {
				fail(fmt.Errorf("read key %d returned seq %d payload %d bytes, want seq %d payload %d bytes",
					key, event.Seq, len(event.Payload), want.seq, want.payload))
				return
			}
			samples = append(samples, rawSample{kind: OpRead, start: start, lat: lat})
		}
		issued++
		if !warmAnnounced && issued == cfg.WarmupOps {
			close(warm)
			warmAnnounced = true
		}
	}
}

// bucketMergeSamples reduces samples to the curve's point sequence:
// windows of a fixed width stepping from from (inclusive) to to
// (exclusive), each point holding the operation counts its window
// caught and each kind's P50/P99 within it by the harness's
// nearest-rank quantile. Samples outside the range are dropped; no
// samples at all means no curve, not a row of invented zero windows.
// Pure: the input slice is not modified.
func bucketMergeSamples(samples []mergeSample, window, from, to time.Duration) []MergeCurvePoint {
	if len(samples) == 0 {
		return nil
	}
	points := make([]MergeCurvePoint, 0, int((to-from)/window)+1)
	var appendLats, readLats []time.Duration
	for off := from; off < to; off += window {
		appendLats = appendLats[:0]
		readLats = readLats[:0]
		end := off + window
		for _, s := range samples {
			if s.offset < off || s.offset >= end {
				continue
			}
			if s.kind == OpAppend {
				appendLats = append(appendLats, s.lat)
			} else {
				readLats = append(readLats, s.lat)
			}
		}
		point := MergeCurvePoint{OffsetNS: int64(off)}
		if n := len(appendLats); n > 0 {
			point.Appends = n
			point.AppendP50NS = int64(quantileOfSorted(sortedCopy(appendLats), 0.5))
			point.AppendP99NS = int64(quantileOfSorted(sortedCopy(appendLats), 0.99))
		}
		if n := len(readLats); n > 0 {
			point.Reads = n
			point.ReadP50NS = int64(quantileOfSorted(sortedCopy(readLats), 0.5))
			point.ReadP99NS = int64(quantileOfSorted(sortedCopy(readLats), 0.99))
		}
		points = append(points, point)
	}
	return points
}

// roundUpWindow rounds elapsed up to the next whole multiple of
// window, so the sampled range covers the merge's full duration in
// whole windows.
func roundUpWindow(elapsed, window time.Duration) time.Duration {
	if elapsed <= 0 {
		return 0
	}
	steps := (elapsed + window - 1) / window
	return steps * window
}

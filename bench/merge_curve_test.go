package bench

import (
	"path/filepath"
	"testing"
	"time"
)

// ms bundles nanoseconds as a duration for readable table entries.
func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// TestBucketMergeSamplesWindowsExact pins the curve's pure core: every
// sample lands in the window its offset addresses, windows run in
// fixed steps from `from` up to `to`, and each window's percentiles
// come off the nearest-rank math the harness already pins.
func TestBucketMergeSamplesWindowsExact(t *testing.T) {
	window := ms(10)
	samples := []mergeSample{
		// Window -20ms..-10ms: two appends.
		{kind: OpAppend, offset: -20 * time.Millisecond, lat: 1 * time.Millisecond},
		{kind: OpAppend, offset: -11 * time.Millisecond, lat: 3 * time.Millisecond},
		// Window -10ms..0: one read at the boundary, one inside.
		{kind: OpRead, offset: -10 * time.Millisecond, lat: 2 * time.Millisecond},
		{kind: OpRead, offset: -1 * time.Millisecond, lat: 4 * time.Millisecond},
		// Window 0..10ms: appends during the merge.
		{kind: OpAppend, offset: 0, lat: 5 * time.Millisecond},
		{kind: OpAppend, offset: 9 * time.Millisecond, lat: 7 * time.Millisecond},
		{kind: OpAppend, offset: 9 * time.Millisecond, lat: 9 * time.Millisecond},
		// Window 10ms..20ms: the merge tail.
		{kind: OpRead, offset: 10 * time.Millisecond, lat: 6 * time.Millisecond},
		// Outside the sampled range on both ends: dropped.
		{kind: OpAppend, offset: -21 * time.Millisecond, lat: 1 * time.Millisecond},
		{kind: OpRead, offset: 20 * time.Millisecond, lat: 1 * time.Millisecond},
	}

	points := bucketMergeSamples(samples, window, -2*window, 2*window)
	if len(points) != 4 {
		t.Fatalf("got %d points, want 4 windows over [-20ms, 20ms)", len(points))
	}
	wantOffsets := []int64{-20000000, -10000000, 0, 10000000}
	type wantPoint struct {
		appends, reads int
		appendP50      time.Duration
		appendP99      time.Duration
		readP50        time.Duration
		readP99        time.Duration
	}
	want := []wantPoint{
		{2, 0, 1 * time.Millisecond, 3 * time.Millisecond, 0, 0},
		{0, 2, 0, 0, 2 * time.Millisecond, 4 * time.Millisecond},
		{3, 0, 7 * time.Millisecond, 9 * time.Millisecond, 0, 0},
		{0, 1, 0, 0, 6 * time.Millisecond, 6 * time.Millisecond},
	}
	for i, p := range points {
		if p.OffsetNS != wantOffsets[i] {
			t.Errorf("point %d offset = %d ns, want %d ns", i, p.OffsetNS, wantOffsets[i])
		}
		w := want[i]
		if p.Appends != w.appends || p.Reads != w.reads {
			t.Errorf("point %d counts = (%d appends, %d reads), want (%d, %d)", i, p.Appends, p.Reads, w.appends, w.reads)
		}
		if p.AppendP50NS != int64(w.appendP50) || p.AppendP99NS != int64(w.appendP99) {
			t.Errorf("point %d append percentiles = (%d, %d) ns, want (%d, %d) ns",
				i, p.AppendP50NS, p.AppendP99NS, w.appendP50, w.appendP99)
		}
		if p.ReadP50NS != int64(w.readP50) || p.ReadP99NS != int64(w.readP99) {
			t.Errorf("point %d read percentiles = (%d, %d) ns, want (%d, %d) ns",
				i, p.ReadP50NS, p.ReadP99NS, w.readP50, w.readP99)
		}
	}
}

// TestBucketMergeSamplesEmptyWindows pins the empty-window convention:
// a window with no operations of one kind carries zero counts and zero
// percentiles — absence, not a zero that pretends to be measured.
func TestBucketMergeSamplesEmptyWindows(t *testing.T) {
	window := ms(5)
	samples := []mergeSample{
		{kind: OpAppend, offset: 0, lat: 1 * time.Millisecond},
		{kind: OpAppend, offset: 11 * time.Millisecond, lat: 2 * time.Millisecond},
	}
	points := bucketMergeSamples(samples, window, 0, 3*window)
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3", len(points))
	}
	middle := points[1]
	if middle.Appends != 0 || middle.Reads != 0 || middle.AppendP50NS != 0 || middle.AppendP99NS != 0 {
		t.Fatalf("empty middle window = %+v, want zero counts and percentiles", middle)
	}
}

// TestBucketMergeSamplesNoSamples pins the degenerate input: no
// samples, no points — never a panic, never an invented window.
func TestBucketMergeSamplesNoSamples(t *testing.T) {
	if points := bucketMergeSamples(nil, ms(10), 0, ms(30)); len(points) != 0 {
		t.Fatalf("got %d points for no samples, want 0", len(points))
	}
}

// TestRunMergeCurveMicro drives the scenario end to end at micro
// scale: a mixed append+read load runs while one explicit public Merge
// executes, and the result is a point sequence in fixed window steps
// that spans the merge window with context on both sides.
func TestRunMergeCurveMicro(t *testing.T) {
	cfg := MergeCurveConfig{
		Sessions:       4,
		ValueSize:      128,
		WarmupOps:      12,
		Window:         2 * time.Millisecond,
		ContextWindows: 2,
		TailWindows:    3,
	}
	got, err := RunMergeCurve(filepath.Join(t.TempDir(), "data"), cfg)
	if err != nil {
		t.Fatalf("RunMergeCurve: %v", err)
	}
	if got.MergeElapsedNS <= 0 {
		t.Fatalf("MergeElapsedNS = %d, want positive", got.MergeElapsedNS)
	}
	if got.ObservedAppends+got.ObservedReads < cfg.WarmupOps {
		t.Fatalf("observed %d ops (%d appends, %d reads), want at least the %d warmup ops",
			got.ObservedAppends+got.ObservedReads, got.ObservedAppends, got.ObservedReads, cfg.WarmupOps)
	}
	if got.ObservedReads == 0 {
		t.Fatal("ObservedReads = 0, want the load to mix reads in")
	}
	if len(got.Points) == 0 {
		t.Fatal("no curve points, want at least the merge window itself")
	}
	windowNS := int64(cfg.Window)
	for i, p := range got.Points {
		wantOffset := -int64(cfg.ContextWindows)*windowNS + int64(i)*windowNS
		if p.OffsetNS != wantOffset {
			t.Fatalf("point %d offset = %d ns, want %d ns (fixed steps of one window)", i, p.OffsetNS, wantOffset)
		}
		if p.Appends < 0 || p.Reads < 0 {
			t.Fatalf("point %d has negative counts: %+v", i, p)
		}
	}
	// The curve spans the whole merge: its last window starts after the
	// merge ended, its first window before it began.
	last := got.Points[len(got.Points)-1]
	if last.OffsetNS < got.MergeElapsedNS {
		t.Fatalf("last window starts at %d ns, merge lasted %d ns, want the curve to reach past the merge",
			last.OffsetNS, got.MergeElapsedNS)
	}
	summed := 0
	for _, p := range got.Points {
		summed += p.Appends + p.Reads
	}
	if summed > got.ObservedAppends+got.ObservedReads {
		t.Fatalf("windows hold %d ops, more than the %d observed", summed, got.ObservedAppends+got.ObservedReads)
	}
}

// TestRunMergeCurveRejectsReuse pins the fresh-directory contract.
func TestRunMergeCurveRejectsReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	cfg := MergeCurveConfig{Sessions: 2, ValueSize: 64, WarmupOps: 4, Window: time.Millisecond, ContextWindows: 1, TailWindows: 1}
	if _, err := RunMergeCurve(dir, cfg); err != nil {
		t.Fatalf("first RunMergeCurve: %v", err)
	}
	if _, err := RunMergeCurve(dir, cfg); err == nil {
		t.Fatal("second RunMergeCurve over the same dir succeeded, want failure")
	}
}

// TestRunMergeCurveValidatesConfig pins the declared shape's guards.
func TestRunMergeCurveValidatesConfig(t *testing.T) {
	base := MergeCurveConfig{Sessions: 2, ValueSize: 64, WarmupOps: 4, Window: time.Millisecond, ContextWindows: 1, TailWindows: 1}
	cases := []struct {
		name string
		mut  func(MergeCurveConfig) MergeCurveConfig
	}{
		{"zero sessions", func(c MergeCurveConfig) MergeCurveConfig { c.Sessions = 0; return c }},
		{"zero value size", func(c MergeCurveConfig) MergeCurveConfig { c.ValueSize = 0; return c }},
		{"zero warmup ops", func(c MergeCurveConfig) MergeCurveConfig { c.WarmupOps = 0; return c }},
		{"zero window", func(c MergeCurveConfig) MergeCurveConfig { c.Window = 0; return c }},
		{"negative context windows", func(c MergeCurveConfig) MergeCurveConfig { c.ContextWindows = -1; return c }},
		{"zero tail windows", func(c MergeCurveConfig) MergeCurveConfig { c.TailWindows = 0; return c }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunMergeCurve(filepath.Join(t.TempDir(), "data"), tc.mut(base)); err == nil {
				t.Fatal("RunMergeCurve accepted an invalid config, want error")
			}
		})
	}
}

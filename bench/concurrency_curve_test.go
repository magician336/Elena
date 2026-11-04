package bench

import (
	"strings"
	"testing"
	"time"

	"elena"
)

// TestRunAppendCurve drives the concurrency curve at smoke size: three
// concurrency steps, each its own fresh store directory so every step
// starts from the same empty-store state, each step's aggregate
// throughput derived from the step's wall clock over every worker's
// events.
func TestRunAppendCurve(t *testing.T) {
	dir := t.TempDir()
	curve, err := RunAppendCurve(dir, CurveSpec{
		Concurrency: []int{1, 2, 4}, OpsPerWorker: 32, ValueSize: 64,
	})
	if err != nil {
		t.Fatalf("RunAppendCurve: %v", err)
	}
	if curve.Driver != "elena" || curve.Workload != "elena-concurrency-curve" {
		t.Fatalf("driver/workload = %q/%q, want elena/elena-concurrency-curve", curve.Driver, curve.Workload)
	}
	if curve.SyncTier != "sync-always" {
		t.Fatalf("sync tier = %q, want sync-always for the default store", curve.SyncTier)
	}
	if len(curve.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(curve.Points))
	}
	for i, want := range []int{1, 2, 4} {
		pt := curve.Points[i]
		if pt.Concurrency != want {
			t.Errorf("point %d concurrency = %d, want %d", i, pt.Concurrency, want)
		}
		if pt.Ops != want*32 {
			t.Errorf("point %d ops = %d, want %d", i, pt.Ops, want*32)
		}
		if pt.Throughput <= 0 {
			t.Errorf("point %d throughput = %g, want positive", i, pt.Throughput)
		}
		if pt.Latency.Count != want*32 {
			t.Errorf("point %d latency count = %d, want %d", i, pt.Latency.Count, want*32)
		}
	}

	// Every step's directory is a real store: reopening step 1's shows
	// its worker's whole trajectory on disk.
	st, err := elena.Open(dir+"/c1", elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("reopen %s/c1: %v", dir, err)
	}
	defer func() { _ = st.Close() }()
	sess, err := st.CreateSession(curveNamespace, curveWorkerName(0))
	if err != nil {
		t.Fatalf("CreateSession worker 0: %v", err)
	}
	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events worker 0: %v", err)
	}
	if len(events) != 32 {
		t.Fatalf("worker 0 holds %d events after reopen, want 32", len(events))
	}
}

// TestRunAppendCurveSyncNone pins the contrast tier: the same curve
// with the store's durability set to no sync, labeled as such — the
// tier that separates fsync serialization from in-memory write-path
// serialization in the curve's reading.
func TestRunAppendCurveSyncNone(t *testing.T) {
	curve, err := RunAppendCurve(t.TempDir(), CurveSpec{
		Concurrency: []int{1, 2}, OpsPerWorker: 16, ValueSize: 64, SyncNone: true,
	})
	if err != nil {
		t.Fatalf("RunAppendCurve: %v", err)
	}
	if curve.SyncTier != "sync-none" {
		t.Fatalf("sync tier = %q, want sync-none", curve.SyncTier)
	}
	if len(curve.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(curve.Points))
	}
}

// TestRunAppendCurveRejectsInvalidSpec pins the spec contract: at
// least one positive concurrency step, positive ops, positive value
// size.
func TestRunAppendCurveRejectsInvalidSpec(t *testing.T) {
	base := CurveSpec{Concurrency: []int{1}, OpsPerWorker: 1, ValueSize: 1}
	cases := []struct {
		name   string
		mutate func(*CurveSpec)
	}{
		{"no steps", func(c *CurveSpec) { c.Concurrency = nil }},
		{"zero step", func(c *CurveSpec) { c.Concurrency = []int{0} }},
		{"negative step", func(c *CurveSpec) { c.Concurrency = []int{2, -1} }},
		{"zero ops", func(c *CurveSpec) { c.OpsPerWorker = 0 }},
		{"zero value size", func(c *CurveSpec) { c.ValueSize = 0 }},
	}
	for _, tc := range cases {
		spec := base
		tc.mutate(&spec)
		if _, err := RunAppendCurve(t.TempDir(), spec); err == nil {
			t.Errorf("%s: RunAppendCurve succeeded, want failure", tc.name)
		}
	}
}

// TestRenderCurveMarkdown pins the curve's rendering: one row per
// concurrency step, a ratio column against the first step, and the
// same input rendering to identical bytes.
func TestRenderCurveMarkdown(t *testing.T) {
	curve := CurveResult{
		Driver:   "elena",
		Workload: "elena-concurrency-curve",
		SyncTier: "sync-none",
		Points: []CurvePoint{
			{Concurrency: 1, Ops: 10, Elapsed: 10 * time.Millisecond, Throughput: 1000, Latency: LatencySummary{Count: 10, P50: time.Millisecond, P99: 2 * time.Millisecond, P999: 3 * time.Millisecond}},
			{Concurrency: 2, Ops: 20, Elapsed: 10 * time.Millisecond, Throughput: 2000, Latency: LatencySummary{Count: 20, P50: 2 * time.Millisecond, P99: 4 * time.Millisecond, P999: 5 * time.Millisecond}},
		},
	}
	md := RenderCurveMarkdown(curve)
	for _, want := range []string{
		"elena", "sync-none", "| concurrency |", "| 1 | 10 |", "| 2 | 20 |", "1.00x", "2.00x",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered curve missing %q:\n%s", want, md)
		}
	}
	if again := RenderCurveMarkdown(curve); again != md {
		t.Errorf("rendering not deterministic:\n%s\nvs\n%s", md, again)
	}
}

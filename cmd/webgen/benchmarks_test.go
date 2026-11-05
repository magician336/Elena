package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The distillation reads the checked-in raw benchmark JSON documents
// from the repository's docs/benchmarks/data; from the package
// directory that tree sits two levels up.
var realDataDir = filepath.Join("..", "..", "docs", "benchmarks", "data")

// approxEqual compares two floats at the precision the distillation
// rounds to; exact equality would re-test float formatting instead of
// the numbers.
func approxEqual(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

// TestQuantizeOpsSec pins the one rounding rule every throughput
// figure passes through: whole ops/sec at a thousand and above, one
// decimal below — the same register the benchmark report reads in.
func TestQuantizeOpsSec(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{473.0789144312876, 473.1},
		{123.30035946278561, 123.3},
		{112618.87767179841, 112619},
		{101313.2, 101313},
		{149.65199911421976, 149.7},
	}
	for _, c := range cases {
		if got := quantizeOpsSec(c.in); !approxEqual(got, c.want) {
			t.Fatalf("quantizeOpsSec(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDistillBenchmarksFromFixture drives the distillation against
// small fixture documents shaped exactly like the checked-in raw
// outputs, with hand-computed expected numbers — an independent
// source of truth for every conversion (ns to ms, bytes to MiB, the
// ratio, the min/max across merge windows).
func TestDistillBenchmarksFromFixture(t *testing.T) {
	got, err := distillBenchmarks(filepath.Join("testdata", "benchmarks"))
	if err != nil {
		t.Fatalf("distillBenchmarks: %v", err)
	}

	if len(got.SameTierWrite.Engines) != 4 {
		t.Fatalf("same-tier write engines = %+v, want the fixture's four", got.SameTierWrite.Engines)
	}
	first, second := got.SameTierWrite.Engines[0], got.SameTierWrite.Engines[1]
	if first.Name != "elena" || !approxEqual(first.OpsSec, 300.5) || !approxEqual(first.P99Ms, 12.5) {
		t.Fatalf("elena figure = %+v, want 300.5 ops/sec with P99 12.5 ms", first)
	}
	if second.Name != "bbolt" || !approxEqual(second.OpsSec, 123.3) || !approxEqual(second.P99Ms, 28.9) {
		t.Fatalf("bbolt figure = %+v, want 123.3 ops/sec with P99 28.9 ms", second)
	}
	third, fourth := got.SameTierWrite.Engines[2], got.SameTierWrite.Engines[3]
	if third.Name != "badger" || !approxEqual(third.OpsSec, 150.5) || !approxEqual(third.P99Ms, 15.0) {
		t.Fatalf("badger figure = %+v, want 150.5 ops/sec with P99 15.0 ms", third)
	}
	if fourth.Name != "rosedb" || !approxEqual(fourth.OpsSec, 140.4) || !approxEqual(fourth.P99Ms, 14.9) {
		t.Fatalf("rosedb figure = %+v, want 140.4 ops/sec with P99 14.9 ms", fourth)
	}
	if got.SameTierWrite.ValueBytes != 1024 {
		t.Fatalf("same-tier value = %d, want 1024", got.SameTierWrite.ValueBytes)
	}

	if !approxEqual(got.ReadOnlyLoss.ElenaOpsSec, 101313) || !approxEqual(got.ReadOnlyLoss.BboltOpsSec, 684356) {
		t.Fatalf("read-only loss = %+v, want 101313 vs 684356 ops/sec", got.ReadOnlyLoss)
	}
	if got.ReadOnlyLoss.ValueBytes != 16384 {
		t.Fatalf("read-only value = %d, want 16384", got.ReadOnlyLoss.ValueBytes)
	}

	if !approxEqual(got.GroupCommit.PerWriteOpsSec, 473.1) || !approxEqual(got.GroupCommit.GroupCommitOpsSec, 112619) {
		t.Fatalf("group commit = %+v, want 473.1 to 112619 ops/sec", got.GroupCommit)
	}

	if !approxEqual(got.MergeWindow.WallMs, 194.8) {
		t.Fatalf("merge wall = %v ms, want 194.8", got.MergeWindow.WallMs)
	}
	if !approxEqual(got.MergeWindow.P50LowMs, 4.1) || !approxEqual(got.MergeWindow.P50HighMs, 4.6) {
		t.Fatalf("merge P50 range = %v–%v ms, want 4.1–4.6 (min and max across the fixture's windows)",
			got.MergeWindow.P50LowMs, got.MergeWindow.P50HighMs)
	}
	if got.MergeWindow.AppendsObserved != 330 || got.MergeWindow.ReadsObserved != 329 {
		t.Fatalf("observed = %d appends / %d reads, want 330 / 329 (the fixture's sums)",
			got.MergeWindow.AppendsObserved, got.MergeWindow.ReadsObserved)
	}

	if !approxEqual(got.StartupPair.HintOpenMs, 295.6) || !approxEqual(got.StartupPair.NoHintOpenMs, 469.7) {
		t.Fatalf("startup pair = %+v, want 295.6 vs 469.7 ms", got.StartupPair)
	}
	if got.StartupPair.LiveEntries != 69632 || !approxEqual(got.StartupPair.DataMB, 273.7) {
		t.Fatalf("startup dataset = %d entries / %v MB, want 69632 / 273.7", got.StartupPair.LiveEntries, got.StartupPair.DataMB)
	}

	if !approxEqual(got.WriteAmp.Before, 1.03) || !approxEqual(got.WriteAmp.After, 2.06) {
		t.Fatalf("write amplification = %+v, want 1.03 to 2.06", got.WriteAmp)
	}

	if !approxEqual(got.SingleWriter.OneWorkerOpsSec, 200000) || !approxEqual(got.SingleWriter.TwoWorkersOpsSec, 120000) {
		t.Fatalf("single writer = %+v, want 200000 to 120000 ops/sec", got.SingleWriter)
	}
	if !approxEqual(got.SingleWriter.Ratio, 0.6) {
		t.Fatalf("single-writer ratio = %v, want 0.6", got.SingleWriter.Ratio)
	}

	if got.LongRun.Ops != 300800 || !approxEqual(got.LongRun.Minutes, 33.5) || !approxEqual(got.LongRun.OpsSec, 149.7) {
		t.Fatalf("long run = %+v, want 300800 ops over 33.5 minutes at 149.7 ops/sec", got.LongRun)
	}
}

// TestDistillBenchmarksAnchorsTheReportNumbers runs the distillation
// against the repository's real checked-in raw outputs and pins the
// numbers the page will show to the values the benchmark report
// cites: whatever the page claims must be traceable to these files.
func TestDistillBenchmarksAnchorsTheReportNumbers(t *testing.T) {
	if _, err := os.Stat(realDataDir); err != nil {
		t.Fatalf("raw benchmark data not found at %s: %v", realDataDir, err)
	}
	got, err := distillBenchmarks(realDataDir)
	if err != nil {
		t.Fatalf("distillBenchmarks: %v", err)
	}

	byEngine := map[string]EngineFigure{}
	for _, fig := range got.SameTierWrite.Engines {
		byEngine[fig.Name] = fig
	}
	elena := byEngine["elena"]
	bbolt := byEngine["bbolt"]
	if len(got.SameTierWrite.Engines) != 4 {
		t.Fatalf("same-tier write has %d engines, want all four", len(got.SameTierWrite.Engines))
	}
	if !approxEqual(elena.OpsSec, 292.3) || !approxEqual(elena.P99Ms, 12.0) {
		t.Fatalf("elena A@1KB = %+v, want 292.3 ops/sec P99 12.0 ms (report table)", elena)
	}
	if !approxEqual(bbolt.OpsSec, 123.3) || !approxEqual(bbolt.P99Ms, 28.9) {
		t.Fatalf("bbolt A@1KB = %+v, want 123.3 ops/sec P99 28.9 ms (report table)", bbolt)
	}
	if !approxEqual(got.GroupCommit.GroupCommitOpsSec, 112619) {
		t.Fatalf("group-commit = %v ops/sec, want 112619 (report table)", got.GroupCommit.GroupCommitOpsSec)
	}
	if !approxEqual(got.MergeWindow.WallMs, 194.8) {
		t.Fatalf("merge wall = %v ms, want 194.8 (report focused metrics)", got.MergeWindow.WallMs)
	}
	if !approxEqual(got.StartupPair.HintOpenMs, 295.6) || !approxEqual(got.StartupPair.NoHintOpenMs, 469.7) {
		t.Fatalf("startup pair = %+v, want 295.6 / 469.7 ms (report focused metrics)", got.StartupPair)
	}
	if !approxEqual(got.WriteAmp.After, 2.06) {
		t.Fatalf("write amp after merge = %v, want 2.06 (report focused metrics)", got.WriteAmp.After)
	}
	if !approxEqual(got.LongRun.OpsSec, 149.7) || got.LongRun.Ops != 300800 {
		t.Fatalf("long run = %+v, want 300800 ops at 149.7 ops/sec (report scenario loads)", got.LongRun)
	}
	if !approxEqual(got.SingleWriter.OneWorkerOpsSec, 323889) || !approxEqual(got.SingleWriter.TwoWorkersOpsSec, 194038) {
		t.Fatalf("single writer = %+v, want 323889 / 194038 ops/sec (curve sync-none)", got.SingleWriter)
	}
	if !approxEqual(got.ReadOnlyLoss.ElenaOpsSec, 101313) || !approxEqual(got.ReadOnlyLoss.BboltOpsSec, 684356) {
		t.Fatalf("read-only loss = %+v, want 101313 vs 684356 (report C mix 16KB)", got.ReadOnlyLoss)
	}
}

// TestDistillBenchmarksErrorsOnMissingFile pins the failure mode: a
// missing raw document is an error, never silently skipped numbers.
func TestDistillBenchmarksErrorsOnMissingFile(t *testing.T) {
	if _, err := distillBenchmarks(t.TempDir()); err == nil {
		t.Fatalf("distilling an empty directory must fail, not pass with zero numbers")
	}
}

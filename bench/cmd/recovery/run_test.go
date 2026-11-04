package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elena/bench/recovery"
)

// cliRoot returns a fresh root directory for one Run-level test: Run
// requires a root that does not exist yet, and a temp-dir child is
// cleaned with the test.
func cliRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "rig-root")
}

// cannedResult is one deterministic result the fake measure hands back.
func cannedResult(spec recovery.Spec) recovery.Result {
	return recovery.Result{
		Events:          spec.Events,
		PayloadSize:     spec.PayloadSize,
		Sessions:        spec.Sessions,
		TargetBytes:     spec.TargetBytes(),
		AckedEvents:     spec.Events,
		KillToOpen:      1500 * time.Microsecond,
		OpenToFirstRead: 500 * time.Microsecond,
		KillToFirstRead: 2 * time.Millisecond,
	}
}

// fakeMeasure records the specs it measured and returns canned results
// — or, with failOn set, the error of that call (1-based).
type fakeMeasure struct {
	specs   []recovery.Spec
	results []recovery.Result
	failOn  int
	err     error
}

func (f *fakeMeasure) measure(spec recovery.Spec) (recovery.Result, error) {
	f.specs = append(f.specs, spec)
	if f.failOn > 0 && len(f.specs) == f.failOn {
		return recovery.Result{}, f.err
	}
	if len(f.results) > 0 {
		res := f.results[0]
		f.results = f.results[1:]
		return res, nil
	}
	return cannedResult(spec), nil
}

// TestRunMeasuresEveryTierRepeat pins the CLI's drive shape: every
// selected tier runs the declared number of times, each run in its own
// store directory under the fresh root, the kill at the hold marker,
// and the output names the tier, its dataset, and all three timing
// segments with the convention declared.
func TestRunMeasuresEveryTierRepeat(t *testing.T) {
	root := cliRoot(t)
	fake := &fakeMeasure{}
	var out bytes.Buffer
	code := Run([]string{"--tier", "micro,256mb", "--runs", "2", "--seed", "7",
		"--dir", root}, &out, new(bytes.Buffer), fake.measure)
	if code != 0 {
		t.Fatalf("Run exited %d with output:\n%s", code, out.String())
	}
	if len(fake.specs) != 4 {
		t.Fatalf("Run measured %d specs, want 2 tiers x 2 runs", len(fake.specs))
	}
	for i, spec := range fake.specs {
		tier := "micro"
		if i >= 2 {
			tier = "256mb"
		}
		wantRun := i%2 + 1
		if want := filepath.Join(root, fmt.Sprintf("%s-run%d", tier, wantRun)); spec.Dir != want {
			t.Fatalf("run %d measured dir %s, want %s", i+1, spec.Dir, want)
		}
		if spec.Seed != 7 || spec.KillAfterAcks != 0 {
			t.Fatalf("run %d spec = %+v, want seed 7 and the hold-marker kill", i+1, spec)
		}
	}
	for _, want := range []string{
		"timing convention: kill->open counts",
		"tier micro (65536 bytes = 128 events x 512B) run 1/2: acked 128",
		"kill->open 1.5ms, open->first-read 500µs, kill->first-read 2ms",
		"tier 256mb (268435456 bytes = 65536 events x 4096B) run 2/2",
		"stability: kill->first-read 2ms vs 2ms, ratio 1.00 — within one order of magnitude",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output misses %q; full output:\n%s", want, out.String())
		}
	}
}

// TestRunValueSizeOverride pins the value-size override: the tier's
// target stays, events re-derive, and a size that does not divide the
// target is a usage error.
func TestRunValueSizeOverride(t *testing.T) {
	fake := &fakeMeasure{}
	var out bytes.Buffer
	code := Run([]string{"--tier", "256mb", "--runs", "1", "--value-size", "1024",
		"--dir", cliRoot(t)}, &out, new(bytes.Buffer), fake.measure)
	if code != 0 {
		t.Fatalf("Run exited %d with output:\n%s", code, out.String())
	}
	spec := fake.specs[0]
	if spec.PayloadSize != 1024 || spec.Events != 256<<20/1024 {
		t.Fatalf("override derived %d events x %dB, want %d x 1024B", spec.Events, spec.PayloadSize, 256<<20/1024)
	}

	code = Run([]string{"--tier", "256mb", "--runs", "1", "--value-size", "1000",
		"--dir", cliRoot(t)}, &out, new(bytes.Buffer), fake.measure)
	if code != 2 {
		t.Fatalf("a non-dividing value size exited %d, want usage error 2", code)
	}
}

// TestRunFailsOnUnstableRepeats pins the stability verdict: repeats of
// one tier more than an order of magnitude apart fail the run, loudly.
func TestRunFailsOnUnstableRepeats(t *testing.T) {
	fast := cannedResult(recovery.Spec{Events: 1, PayloadSize: 1})
	fast.KillToOpen = time.Millisecond
	fast.OpenToFirstRead = time.Millisecond
	fast.KillToFirstRead = 2 * time.Millisecond
	slow := fast
	slow.KillToOpen = 5 * time.Second
	slow.OpenToFirstRead = 5 * time.Second
	slow.KillToFirstRead = 10 * time.Second
	fake := &fakeMeasure{results: []recovery.Result{fast, slow}}
	var out, errOut bytes.Buffer
	code := Run([]string{"--tier", "micro", "--runs", "2", "--dir", cliRoot(t)},
		&out, &errOut, fake.measure)
	if code != 1 {
		t.Fatalf("an unstable tier exited %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "order of magnitude") {
		t.Fatalf("stderr = %q, want the instability verdict", errOut.String())
	}
	if !strings.Contains(out.String(), "ratio 5000.00") {
		t.Fatalf("output = %q, want the 5000.00 ratio", out.String())
	}
}

// TestRunSurfacesMeasureFailure pins that a failed measurement — a
// correctness violation in the rig itself — fails the run with the
// rig's error verbatim.
func TestRunSurfacesMeasureFailure(t *testing.T) {
	fake := &fakeMeasure{failOn: 1, err: fmt.Errorf("acked event s0#3 lost")}
	var out, errOut bytes.Buffer
	code := Run([]string{"--tier", "micro", "--runs", "1", "--dir", cliRoot(t)},
		&out, &errOut, fake.measure)
	if code != 1 || !strings.Contains(errOut.String(), "s0#3 lost") {
		t.Fatalf("Run = (%d, %q), want the measure failure surfaced", code, errOut.String())
	}
}

// TestParseRejectsBadArguments pins the argument contract: the root
// directory and tiers are required, runs and value size are positive,
// and every tier name must be known.
func TestParseRejectsBadArguments(t *testing.T) {
	base := []string{"--tier", "micro", "--dir", cliRoot(t)}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing dir", []string{"--tier", "micro"}, "--dir"},
		{"unknown tier", append(base, "--tier", "1tb"), "unknown"},
		{"zero runs", append(append([]string{}, base...), "--runs", "0"), "--runs"},
		{"negative value size", append(append([]string{}, base...), "--value-size", "-1"), "--value-size"},
		{"positional junk", append(append([]string{}, base...), "extra"), "unexpected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse(%v) = %v, want an error mentioning %q", tc.args, err, tc.want)
			}
		})
	}
	cfg, err := parse(base)
	if err != nil {
		t.Fatalf("parse of %v: %v", base, err)
	}
	if len(cfg.tiers) != 1 || cfg.tiers[0] != "micro" || cfg.runs != 2 || cfg.seed != 1 || cfg.valueSize != 0 {
		t.Fatalf("parse defaults = %+v, want micro, 2 runs, seed 1, tier value size", cfg)
	}
}

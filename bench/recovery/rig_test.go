package recovery

import (
	"strings"
	"testing"
	"time"

	"elena"
)

// microSpec is the CI-scale rig shape: a seconds-scale schedule under
// the always-sync regime, killed at the hold marker.
func microSpec(dir string) Spec {
	return Spec{Dir: dir, Sessions: 4, Events: 48, PayloadSize: 512, Seed: 5}
}

// TestMeasureMicroTier pins the rig end to end through the public
// elena API at CI scale: the child appends and acknowledges the whole
// schedule, the parent kills it at the hold marker, and the reopened
// directory passes the kill -9 correctness bar — every acknowledged
// event survives byte-exact — while the result carries the declared
// timing segments.
func TestMeasureMicroTier(t *testing.T) {
	maybeSkipKill9(t)
	dir := t.TempDir()

	res, err := Measure(microSpec(dir))
	if err != nil {
		t.Fatalf("Measure of the micro tier failed: %v", err)
	}
	if res.AckedEvents != 48 {
		t.Fatalf("result acknowledges %d events, want the full 48", res.AckedEvents)
	}
	if res.Events != 48 || res.PayloadSize != 512 || res.Sessions != 4 || res.TargetBytes != 48*512 {
		t.Fatalf("result dataset fields = %+v, want the spec's shape", res)
	}
	assertSegments(t, res)
}

// TestMeasureMidRunKill pins the mid-run kill shape: the parent kills
// after an arbitrary acknowledged event, the acknowledged events —
// at least the kill point, at most the schedule — all survive the
// reopen, and the timing segments still come out.
func TestMeasureMidRunKill(t *testing.T) {
	maybeSkipKill9(t)
	spec := microSpec(t.TempDir())
	spec.Events = 12
	spec.KillAfterAcks = 5

	res, err := Measure(spec)
	if err != nil {
		t.Fatalf("Measure with a mid-run kill failed: %v", err)
	}
	if res.AckedEvents < 5 || res.AckedEvents > 12 {
		t.Fatalf("result acknowledges %d events, want within [5, 12]", res.AckedEvents)
	}
	assertSegments(t, res)
}

// TestMeasureRejectsInvalidSpec pins that the rig refuses specs the
// child or the parent cannot run with, before any process starts.
func TestMeasureRejectsInvalidSpec(t *testing.T) {
	spec := microSpec(t.TempDir())
	spec.KillAfterAcks = 13
	spec.Events = 12
	if _, err := Measure(spec); err == nil || !strings.Contains(err.Error(), "KillAfterAcks") {
		t.Fatalf("Measure of an invalid spec = %v, want the KillAfterAcks rejection", err)
	}
}

// TestMeasureStabilityMicroTier pins the stability bar at CI scale:
// two runs of the same tier shape recover within one order of
// magnitude — the assertion the tier report's repeats rest on.
func TestMeasureStabilityMicroTier(t *testing.T) {
	maybeSkipKill9(t)

	first, err := Measure(microSpec(t.TempDir()))
	if err != nil {
		t.Fatalf("first stability run failed: %v", err)
	}
	second, err := Measure(microSpec(t.TempDir()))
	if err != nil {
		t.Fatalf("second stability run failed: %v", err)
	}
	if !SameMagnitude(first.KillToFirstRead, second.KillToFirstRead) {
		t.Fatalf("two runs of one tier recovered in %v and %v — not the same order of magnitude",
			first.KillToFirstRead, second.KillToFirstRead)
	}
}

// assertSegments checks the declared timing shape of every result:
// both segments positive and the total exactly their sum.
func assertSegments(t *testing.T, res Result) {
	t.Helper()
	if res.KillToOpen <= 0 || res.OpenToFirstRead <= 0 {
		t.Fatalf("timing segments kill->open %v, open->first read %v — both must be positive",
			res.KillToOpen, res.OpenToFirstRead)
	}
	if res.KillToFirstRead != res.KillToOpen+res.OpenToFirstRead {
		t.Fatalf("kill->first read %v != kill->open %v + open->first read %v",
			res.KillToFirstRead, res.KillToOpen, res.OpenToFirstRead)
	}
}

// TestSameMagnitude pins the stability bar itself: durations within a
// factor of ten agree, anything wider apart or non-positive does not.
func TestSameMagnitude(t *testing.T) {
	cases := []struct {
		a, b time.Duration
		want bool
	}{
		{1 * time.Millisecond, 9 * time.Millisecond, true},
		{9 * time.Millisecond, 1 * time.Millisecond, true},
		{10 * time.Millisecond, 1 * time.Millisecond, true},
		{11 * time.Millisecond, 1 * time.Millisecond, false},
		{2 * time.Second, 200 * time.Millisecond, true},
		{2 * time.Second, 100 * time.Millisecond, false},
		{0, 5 * time.Millisecond, false},
		{5 * time.Millisecond, 0, false},
		{-1 * time.Second, 5 * time.Millisecond, false},
	}
	for _, tc := range cases {
		if got := SameMagnitude(tc.a, tc.b); got != tc.want {
			t.Fatalf("SameMagnitude(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestVerifyAckedSurviveRejectsLoss pins the verification's failure
// side, the assertion shape the kill -9 suites share: an acknowledged
// event missing from the reopened store fails the measurement, and so
// does a payload that is not the exact dataset bytes.
func TestVerifyAckedSurviveRejectsLoss(t *testing.T) {
	// A store written from a different seed: same shape, wrong bytes.
	otherDir := t.TempDir()
	other := Spec{Dir: otherDir, Sessions: 1, Events: 4, PayloadSize: 32, Seed: 9}
	stOther, err := elena.Open(otherDir)
	if err != nil {
		t.Fatalf("open %s: %v", otherDir, err)
	}
	defer stOther.Close()
	if err := appendSchedule(stOther, other, func(Ack) error { return nil }); err != nil {
		t.Fatalf("appendSchedule: %v", err)
	}
	spec := other
	spec.Seed = 8
	if err := verifyAckedSurvive(stOther, spec, []uint64{4}); err == nil || !strings.Contains(err.Error(), "payload bytes") {
		t.Fatalf("a corrupted dataset passed verification: %v", err)
	}

	// A store written from the spec itself: the honest run passes, a
	// claimed sequence beyond the schedule is a lost acknowledgment.
	dir := t.TempDir()
	spec.Dir = dir
	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	defer st.Close()
	if err := appendSchedule(st, spec, func(Ack) error { return nil }); err != nil {
		t.Fatalf("appendSchedule: %v", err)
	}
	if err := verifyAckedSurvive(st, spec, []uint64{4}); err != nil {
		t.Fatalf("the honest dataset failed verification: %v", err)
	}
	if err := verifyAckedSurvive(st, spec, []uint64{5}); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatalf("a lost acknowledgment passed verification: %v", err)
	}
	if err := verifyAckedSurvive(st, spec, []uint64{0}); err != nil {
		t.Fatalf("an empty acknowledged run failed verification: %v", err)
	}
	closeMeasured(st) // idempotent on an open store; also covers the helper
}

// TestVerifyPostKillWriteRejectsRegression pins the post-kill write
// check: an append that does not continue past the acknowledged run,
// and a check with no second life on a closed store, fail.
func TestVerifyPostKillWriteRejectsRegression(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Dir: dir, Sessions: 1, Events: 2, PayloadSize: 16, Seed: 8}

	st, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	if err := appendSchedule(st, spec, func(Ack) error { return nil }); err != nil {
		t.Fatalf("appendSchedule: %v", err)
	}
	// The acknowledged maximum claims sequences the trajectory cannot
	// continue past: the append takes 3, not beyond 5 — wrong only
	// because the fixture lied about what was acknowledged.
	if err := verifyPostKillWrite(st, dir, []uint64{5}); err == nil || !strings.Contains(err.Error(), "at or below") {
		t.Fatalf("a regressing post-kill append passed: %v", err)
	}
	closeMeasured(st) // the failed check leaves the store open; free the directory

	// The honest run closes its store on success; a second call on the
	// closed store fails — the check has no second life.
	st2, err := elena.Open(dir)
	if err != nil {
		t.Fatalf("reopen %s: %v", dir, err)
	}
	if err := verifyPostKillWrite(st2, dir, []uint64{2}); err != nil {
		t.Fatalf("the honest post-kill write failed: %v", err)
	}
	if err := verifyPostKillWrite(st2, dir, []uint64{2}); err == nil {
		t.Fatal("a post-kill check on a closed store succeeded, want the closed error")
	}
}

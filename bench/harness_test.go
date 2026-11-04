package bench

import (
	"errors"
	"fmt"
	"testing"
)

// fakeDriver records the harness's calls and keeps the appended values
// in memory: the read phase then proves the harness drove the append
// phase first — a read hitting a missing key fails the run.
type fakeDriver struct {
	calls  []string
	values map[uint64][]byte

	openFail  bool
	failOnDo  int // 1-based Do call to fail; 0 never fails
	closeFail bool
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{values: map[uint64][]byte{}}
}

func (f *fakeDriver) Name() string { return "fake" }

func (f *fakeDriver) Open(dir string) error {
	f.calls = append(f.calls, "open "+dir)
	if f.openFail {
		return errors.New("open failed")
	}
	return nil
}

func (f *fakeDriver) Do(op Op) error {
	f.calls = append(f.calls, fmt.Sprintf("%s %d", op.Kind, op.Key))
	if f.failOnDo > 0 && len(f.calls) == f.failOnDo+1 {
		return fmt.Errorf("do failed on %s %d", op.Kind, op.Key)
	}
	switch op.Kind {
	case OpAppend:
		f.values[op.Key] = op.Value
	case OpRead:
		if _, ok := f.values[op.Key]; !ok {
			return fmt.Errorf("read %d before any append of it", op.Key)
		}
	}
	return nil
}

func (f *fakeDriver) Close() error {
	f.calls = append(f.calls, "close")
	if f.closeFail {
		return errors.New("close failed")
	}
	return nil
}

// microForTest builds a small micro workload or fails the test.
func microForTest(t *testing.T) Workload {
	t.Helper()
	w, err := Micro(3, 8)
	if err != nil {
		t.Fatalf("Micro: %v", err)
	}
	return w
}

// TestRunDrivesLifecycleInOrder pins the harness's one contract: open
// once, drive every phase's operations in workload order, close once —
// the append phase before the read phase, so the fake driver's
// read-before-append guard proves the sequencing.
func TestRunDrivesLifecycleInOrder(t *testing.T) {
	drv := newFakeDriver()
	res, err := Run(drv, t.TempDir(), microForTest(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		"open", // dir appended below
		"append 0", "append 1", "append 2",
		"read 0", "read 1", "read 2",
		"close",
	}
	got := drv.calls
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v (open/close wrapping the ops)", got, want)
	}
	if got[0] == "" || got[0][:5] != "open " {
		t.Fatalf("first call = %q, want open <dir>", got[0])
	}
	if got[len(got)-1] != "close" {
		t.Fatalf("last call = %q, want close", got[len(got)-1])
	}
	ops := append(got[1:4], got[4:7]...)
	for i, w := range append(want[1:4], want[4:7]...) {
		if ops[i] != w {
			t.Fatalf("op call %d = %q, want %q", i, ops[i], w)
		}
	}
	if res.Driver != "fake" || res.Workload != "micro" {
		t.Fatalf("result names = %q/%q, want fake/micro", res.Driver, res.Workload)
	}
}

// TestRunCollectsPerPhaseNumbers pins the observability contract: every
// phase reports its operation count, a positive elapsed time, a
// matching sample count, and a throughput derived from them.
func TestRunCollectsPerPhaseNumbers(t *testing.T) {
	drv := newFakeDriver()
	res, err := Run(drv, t.TempDir(), microForTest(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Phases) != 2 {
		t.Fatalf("phase results = %d, want 2", len(res.Phases))
	}
	for i, wantName := range []string{"append", "read"} {
		pr := res.Phases[i]
		if pr.Phase != wantName {
			t.Fatalf("phase %d name = %q, want %q", i, pr.Phase, wantName)
		}
		if pr.Ops != 3 {
			t.Fatalf("phase %q ops = %d, want 3", pr.Phase, pr.Ops)
		}
		if pr.Latency.Count != 3 {
			t.Fatalf("phase %q latency count = %d, want 3", pr.Phase, pr.Latency.Count)
		}
		if pr.Latency.Min <= 0 || pr.Latency.Max <= 0 || pr.Latency.Mean <= 0 {
			t.Fatalf("phase %q latency summary has non-positive entries: %+v", pr.Phase, pr.Latency)
		}
		if pr.Elapsed <= 0 {
			t.Fatalf("phase %q elapsed = %v, want positive", pr.Phase, pr.Elapsed)
		}
		if pr.Throughput <= 0 {
			t.Fatalf("phase %q throughput = %v, want positive", pr.Phase, pr.Throughput)
		}
	}
}

// TestRunClosesAfterOpen pins the failure contract: an Open failure
// surfaces and Close is never called on an unopened driver, while a
// mid-phase Do failure surfaces with Close still called — an opened
// store is never left behind by the harness.
func TestRunClosesAfterOpen(t *testing.T) {
	t.Run("open failure skips do and close", func(t *testing.T) {
		drv := newFakeDriver()
		drv.openFail = true
		if _, err := Run(drv, t.TempDir(), microForTest(t)); err == nil {
			t.Fatal("Run succeeded despite open failure")
		}
		if len(drv.calls) != 1 || drv.calls[0][:4] != "open" {
			t.Fatalf("calls = %v, want exactly the failed open", drv.calls)
		}
	})
	t.Run("do failure still closes", func(t *testing.T) {
		drv := newFakeDriver()
		drv.failOnDo = 2 // second Do call fails
		if _, err := Run(drv, t.TempDir(), microForTest(t)); err == nil {
			t.Fatal("Run succeeded despite do failure")
		}
		if drv.calls[len(drv.calls)-1] != "close" {
			t.Fatalf("last call = %q, want close after mid-run failure", drv.calls[len(drv.calls)-1])
		}
	})
	t.Run("close failure surfaces", func(t *testing.T) {
		drv := newFakeDriver()
		drv.closeFail = true
		if _, err := Run(drv, t.TempDir(), microForTest(t)); err == nil {
			t.Fatal("Run succeeded despite close failure")
		}
	})
}

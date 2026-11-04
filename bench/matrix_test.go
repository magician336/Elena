package bench

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// matrixSpecForTest is one small valid spec, the base the validation
// and planning tests below tweak one field at a time.
func matrixSpecForTest() MatrixSpec {
	return MatrixSpec{
		Engines:    []string{"elena", "bbolt"},
		Mixes:      []YCSB{YCSBA},
		ValueSizes: []int{64, 1024},
		Keys:       16,
		RunOps:     32,
		Seed:       1,
		SyncPeriod: 10 * time.Millisecond,
	}
}

// TestFullMatrixDeclaresEverything pins the declared full matrix: every
// registered engine, the three YCSB mixes, the five value-size steps —
// the axes one command runs.
func TestFullMatrixDeclaresEverything(t *testing.T) {
	spec := FullMatrix(1, 1000, 5000, 10*time.Millisecond)
	if !reflect.DeepEqual(spec.Engines, Drivers()) {
		t.Fatalf("engines = %v, want every registered driver %v", spec.Engines, Drivers())
	}
	if !reflect.DeepEqual(spec.Mixes, []YCSB{YCSBA, YCSBB, YCSBC}) {
		t.Fatalf("mixes = %v, want the three YCSB mixes", spec.Mixes)
	}
	if !reflect.DeepEqual(spec.ValueSizes, []int{64, 256, 1024, 4096, 16384}) {
		t.Fatalf("value sizes = %v, want the five declared steps", spec.ValueSizes)
	}
	if spec.Dist != KeyZipf || spec.ZipfTheta != 0.99 {
		t.Fatalf("key distribution = %v theta %g, want YCSB's zipfian default", spec.Dist, spec.ZipfTheta)
	}
}

// TestPlanMatrixEnumeratesCells pins the plan's enumeration: every
// engine×mix×size combination at every tier the engine supports, in a
// fixed order — engine, then per-write tier ahead of group-commit, then
// mix, then value size. An unsupported tier contributes no cells.
func TestPlanMatrixEnumeratesCells(t *testing.T) {
	cells, _, err := PlanMatrix(matrixSpecForTest())
	if err != nil {
		t.Fatalf("PlanMatrix: %v", err)
	}
	want := []Cell{
		{Engine: "elena", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 64},
		{Engine: "elena", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 1024},
		{Engine: "elena", Tier: SyncGroupCommit, Mix: YCSBA, ValueSize: 64},
		{Engine: "elena", Tier: SyncGroupCommit, Mix: YCSBA, ValueSize: 1024},
		{Engine: "bbolt", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 64},
		{Engine: "bbolt", Tier: SyncPerWrite, Mix: YCSBA, ValueSize: 1024},
	}
	if !reflect.DeepEqual(cells, want) {
		t.Fatalf("cells =\n%v\nwant\n%v", cells, want)
	}
}

// TestPlanMatrixDeclaresAlignmentPerEngine pins the per-engine
// durability declaration: every engine states its standing under both
// tiers — the configuration sentence when aligned, the reason no
// numbers exist when not — so the report declares the alignment engine
// by engine.
func TestPlanMatrixDeclaresAlignmentPerEngine(t *testing.T) {
	_, alignment, err := PlanMatrix(matrixSpecForTest())
	if err != nil {
		t.Fatalf("PlanMatrix: %v", err)
	}
	if len(alignment) != 2 {
		t.Fatalf("alignment entries = %d, want 2 (one per engine)", len(alignment))
	}
	byEngine := map[string]EngineAlignment{}
	for _, a := range alignment {
		byEngine[a.Engine] = a
	}
	for _, engine := range []string{"elena", "bbolt"} {
		a, ok := byEngine[engine]
		if !ok {
			t.Fatalf("engine %q missing from the alignment declaration", engine)
		}
		if len(a.Tiers) != 2 {
			t.Fatalf("engine %q declares %d tiers, want 2", engine, len(a.Tiers))
		}
		for _, td := range a.Tiers {
			if td.Configuration == "" {
				t.Fatalf("engine %q tier %s declares an empty configuration", engine, td.Tier)
			}
		}
	}
	for _, td := range byEngine["elena"].Tiers {
		if !td.Aligned {
			t.Fatalf("elena tier %s is declared unaligned, want aligned", td.Tier)
		}
	}
	for _, td := range byEngine["bbolt"].Tiers {
		want := td.Tier == SyncPerWrite
		if td.Aligned != want {
			t.Fatalf("bbolt tier %s aligned = %v, want %v", td.Tier, td.Aligned, want)
		}
	}
}

// TestMatrixCellDirName pins the cell's directory name: engine, tier,
// mix, value size — readable, unique per cell, and usable as one fresh
// directory under the run root.
func TestMatrixCellDirName(t *testing.T) {
	cell := Cell{Engine: "elena", Tier: SyncGroupCommit, Mix: YCSBA, ValueSize: 64}
	if got, want := cell.DirName(), "elena-group-commit-ycsb-a-64B"; got != want {
		t.Fatalf("DirName() = %q, want %q", got, want)
	}
	cells, _, err := PlanMatrix(matrixSpecForTest())
	if err != nil {
		t.Fatalf("PlanMatrix: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range cells {
		if seen[c.DirName()] {
			t.Fatalf("duplicate dir name %q", c.DirName())
		}
		seen[c.DirName()] = true
	}
}

// TestPlanMatrixInvalidSpec pins the validation surface: a bad axis is
// an error before any cell runs, including a missing group-commit
// period exactly when the plan needs one.
func TestPlanMatrixInvalidSpec(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*MatrixSpec)
	}{
		{"no engines", func(s *MatrixSpec) { s.Engines = nil }},
		{"unknown engine", func(s *MatrixSpec) { s.Engines = []string{"elena", "pebble"} }},
		{"no mixes", func(s *MatrixSpec) { s.Mixes = nil }},
		{"unknown mix", func(s *MatrixSpec) { s.Mixes = []YCSB{YCSB(9)} }},
		{"no value sizes", func(s *MatrixSpec) { s.ValueSizes = nil }},
		{"zero value size", func(s *MatrixSpec) { s.ValueSizes = []int{64, 0} }},
		{"zero keys", func(s *MatrixSpec) { s.Keys = 0 }},
		{"zero run ops", func(s *MatrixSpec) { s.RunOps = 0 }},
		{"negative run ops", func(s *MatrixSpec) { s.RunOps = -1 }},
		{"no group-commit period where needed", func(s *MatrixSpec) { s.SyncPeriod = 0 }},
		{"zipf without theta", func(s *MatrixSpec) { s.Dist = KeyZipf; s.ZipfTheta = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := matrixSpecForTest()
			tt.mut(&spec)
			if _, _, err := PlanMatrix(spec); err == nil {
				t.Fatal("PlanMatrix succeeded on an invalid spec, want error")
			}
		})
	}
}

// TestPlanMatrixPeriodOptionalWithoutGroupCommit pins that the
// group-commit period is validated only when the plan actually carries
// a group-commit cell: an all-KV spec with no period is valid.
func TestPlanMatrixPeriodOptionalWithoutGroupCommit(t *testing.T) {
	spec := matrixSpecForTest()
	spec.Engines = []string{"bbolt"}
	spec.SyncPeriod = 0
	if _, _, err := PlanMatrix(spec); err != nil {
		t.Fatalf("PlanMatrix without group-commit cells: %v", err)
	}
}

// recordingDriver is a fake driver the orchestration tests inject: it
// records every operation it executes, proving the sequence each engine
// drove, and can fail on demand.
type recordingDriver struct {
	name  string
	ops   []string
	fail  bool
	opens int
}

func (r *recordingDriver) Name() string { return r.name }

func (r *recordingDriver) Open(dir string) error {
	r.opens++
	if r.fail {
		return fmt.Errorf("open %s failed as asked", r.name)
	}
	return nil
}

func (r *recordingDriver) Do(op Op) error {
	r.ops = append(r.ops, fmt.Sprintf("%s %d", op.Kind, op.Key))
	return nil
}

func (r *recordingDriver) Close() error { return nil }

// fakeFactory builds one recording driver per requested engine and
// remembers them by cell directory name; engines listed in failEngines
// get failing drivers.
type fakeFactory struct {
	built       map[string]*recordingDriver // dir name → driver
	failEngines map[string]bool
}

func newFakeFactory(failEngines ...string) *fakeFactory {
	f := &fakeFactory{built: map[string]*recordingDriver{}, failEngines: map[string]bool{}}
	for _, e := range failEngines {
		f.failEngines[e] = true
	}
	return f
}

func (f *fakeFactory) new(name string, tier SyncTier, period time.Duration) (Driver, error) {
	drv := &recordingDriver{name: name, fail: f.failEngines[name]}
	key := name + "/" + tier.String()
	f.built[key] = drv
	return drv, nil
}

// runMatrixForTest runs the small spec through runMatrix with a fake
// factory or fails the test.
func runMatrixForTest(t *testing.T, spec MatrixSpec, factory func(string, SyncTier, time.Duration) (Driver, error)) MatrixResult {
	t.Helper()
	res, err := runMatrix(spec, t.TempDir(), factory)
	if err != nil {
		t.Fatalf("runMatrix: %v", err)
	}
	return res
}

// TestRunMatrixDrivesSameSequencePerEngine pins the same-seed contract
// where it matters: every engine at the per-write tier drives the
// identical load-then-run operation sequence — the recording drivers
// prove driver differences never leak into workload semantics — each in
// its own fresh directory under the root.
func TestRunMatrixDrivesSameSequencePerEngine(t *testing.T) {
	spec := matrixSpecForTest()
	spec.ValueSizes = []int{64}
	factory := newFakeFactory()
	res := runMatrixForTest(t, spec, factory.new)

	if len(res.Cells) != 3 { // elena per-write, elena group-commit, bbolt per-write
		t.Fatalf("cells run = %d, want 3", len(res.Cells))
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %v, want none", res.Failures)
	}

	elena := factory.built["elena/"+SyncPerWrite.String()]
	bbolt := factory.built["bbolt/"+SyncPerWrite.String()]
	if elena == nil || bbolt == nil {
		t.Fatalf("factory built = %v keys, want elena and bbolt per-write", len(factory.built))
	}
	if !reflect.DeepEqual(elena.ops, bbolt.ops) {
		t.Fatal("elena and bbolt drove different operation sequences")
	}
	if len(elena.ops) != int(spec.Keys)+spec.RunOps {
		t.Fatalf("ops driven = %d, want %d (load + run)", len(elena.ops), int(spec.Keys)+spec.RunOps)
	}
	for _, drv := range factory.built {
		if drv.opens != 1 {
			t.Fatalf("driver %s opened %d times, want exactly once", drv.name, drv.opens)
		}
	}
}

// TestRunMatrixFreshDirectoryPerCell pins the directory contract: every
// cell runs in its own fresh subdirectory under the root, named by the
// cell — a rerun over leftovers is not a measurement.
func TestRunMatrixFreshDirectoryPerCell(t *testing.T) {
	spec := matrixSpecForTest()
	root := t.TempDir()
	factory := newFakeFactory()
	res, err := runMatrix(spec, root, factory.new)
	if err != nil {
		t.Fatalf("runMatrix: %v", err)
	}

	if len(res.Cells) != 6 {
		t.Fatalf("cells run = %d, want 6", len(res.Cells))
	}
	for _, cr := range res.Cells {
		dir := filepath.Join(root, cr.Cell.DirName())
		if factory.built[cr.Cell.Engine+"/"+cr.Cell.Tier.String()].opens != 1 {
			t.Fatalf("cell %v did not open its driver", cr.Cell)
		}
		if !strings.HasPrefix(dir, root) {
			t.Fatalf("cell directory %q is not under root %q", dir, root)
		}
	}
	// A rerun over the same root hits existing directories and records
	// failures instead of reusing leftovers.
	rerun, err := runMatrix(spec, root, factory.new)
	if err != nil {
		t.Fatalf("runMatrix rerun: %v", err)
	}
	if len(rerun.Failures) != 6 {
		t.Fatalf("rerun failures = %d, want 6 (every directory exists)", len(rerun.Failures))
	}
	if len(rerun.Cells) != 0 {
		t.Fatalf("rerun cells = %d, want 0 (leftovers produce no numbers)", len(rerun.Cells))
	}
}

// TestRunMatrixRecordsCellFailures pins the failure contract: one
// engine's failure is recorded against its cells and the matrix keeps
// running — the surviving engines still produce their numbers, and the
// failures carry the cells they belong to.
func TestRunMatrixRecordsCellFailures(t *testing.T) {
	spec := matrixSpecForTest()
	spec.ValueSizes = []int{64}
	factory := newFakeFactory("bbolt")
	res := runMatrixForTest(t, spec, factory.new)

	if len(res.Cells) != 2 { // elena's two tiers survive
		t.Fatalf("cells run = %d, want 2", len(res.Cells))
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(res.Failures))
	}
	f := res.Failures[0]
	if f.Cell.Engine != "bbolt" || f.Err == nil {
		t.Fatalf("failure = %+v, want bbolt with its error", f)
	}
	for _, cr := range res.Cells {
		if cr.Cell.Engine == "bbolt" {
			t.Fatalf("bbolt produced numbers despite its recorded failure: %+v", cr)
		}
	}
}

// TestRunMatrixRejectsInvalidSpec pins that an invalid spec fails
// before any driver is built.
func TestRunMatrixRejectsInvalidSpec(t *testing.T) {
	spec := matrixSpecForTest()
	spec.RunOps = 0
	called := false
	_, err := runMatrix(spec, t.TempDir(), func(string, SyncTier, time.Duration) (Driver, error) {
		called = true
		return &recordingDriver{}, nil
	})
	if err == nil {
		t.Fatal("runMatrix succeeded on an invalid spec, want error")
	}
	if called {
		t.Fatal("runMatrix built a driver despite the invalid spec")
	}
}

// TestRunMatrixEndToEndSmoke drives the real matrix end to end on a
// smoke-scale spec: every registered engine, one mix, one value size,
// a handful of operations — the seconds-scale run CI repeats on every
// push, proof the harness does not rot as engines evolve.
func TestRunMatrixEndToEndSmoke(t *testing.T) {
	spec := MatrixSpec{
		Engines:    Drivers(),
		Mixes:      []YCSB{YCSBA},
		ValueSizes: []int{64},
		Keys:       8,
		RunOps:     8,
		Seed:       3,
		SyncPeriod: 5 * time.Millisecond,
	}
	res, err := RunMatrix(spec, t.TempDir())
	if err != nil {
		t.Fatalf("RunMatrix: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %v, want none", res.Failures)
	}
	if len(res.Cells) != 5 { // elena×2 tiers + bbolt + badger + rosedb
		t.Fatalf("cells = %d, want 5", len(res.Cells))
	}
	for _, cr := range res.Cells {
		if len(cr.Result.Phases) != 2 {
			t.Fatalf("cell %v phases = %d, want load+run", cr.Cell, len(cr.Result.Phases))
		}
		load, run := cr.Result.Phases[0], cr.Result.Phases[1]
		if load.Phase != phaseLoad || load.Ops != 8 || run.Phase != phaseRun || run.Ops != 8 {
			t.Fatalf("cell %v phases = %+v %+v, want load 8 then run 8", cr.Cell, load, run)
		}
	}
}

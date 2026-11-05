package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// matrixValueSizes are the five value-size steps of the declared full
// matrix; matrixSyncTiers are the durability tiers it aligns on, in
// report order.
var (
	matrixValueSizes = []int{64, 256, 1024, 4096, 16384}
	matrixSyncTiers  = []SyncTier{SyncPerWrite, SyncGroupCommit}
)

// MatrixSpec declares the comparison matrix: which engines at which
// durability tiers, which YCSB mixes, which value sizes, one key space
// and its distribution, the run phase's operation count, one seed, and
// the group-commit period for the engines that run that tier. One seed
// selects every workload: each engine executes the identical
// operation sequences.
type MatrixSpec struct {
	// Engines names the drivers to run, in run order; every name must
	// resolve through ForTier.
	Engines []string

	// Mixes selects the YCSB workloads to run.
	Mixes []YCSB

	// ValueSizes selects the value-size steps, in bytes per appended
	// value.
	ValueSizes []int

	// Keys is the key space every workload draws from; the load phase
	// appends exactly one value per key. Must be positive.
	Keys uint64

	// Dist selects the run phases' key distribution. The default
	// matrix runs YCSB's zipfian (theta 0.99).
	Dist KeyDist

	// ZipfTheta shapes Dist KeyZipf; required positive when Dist is
	// KeyZipf.
	ZipfTheta float64

	// RunOps is every run phase's operation count; must be positive.
	RunOps int

	// Seed selects every workload's operation sequences.
	Seed uint64

	// SyncPeriod is the group-commit period, required positive exactly
	// when some engine in Engines runs that tier.
	SyncPeriod time.Duration
}

// FullMatrix returns the declared full comparison: every registered
// engine, the three YCSB mixes, the five value-size steps, keys drawn
// from YCSB's zipfian (theta 0.99).
func FullMatrix(seed, keys uint64, runOps int, period time.Duration) MatrixSpec {
	return MatrixSpec{
		Engines:    Drivers(),
		Mixes:      []YCSB{YCSBA, YCSBB, YCSBC},
		ValueSizes: append([]int(nil), matrixValueSizes...),
		Keys:       keys,
		Dist:       KeyZipf,
		ZipfTheta:  0.99,
		RunOps:     runOps,
		Seed:       seed,
		SyncPeriod: period,
	}
}

// Cell identifies one runnable combination of the matrix: an engine at
// one durability tier, one mix, one value size.
type Cell struct {
	Engine    string
	Tier      SyncTier
	Mix       YCSB
	ValueSize int
}

// DirName is the cell's fresh-directory name under the run root:
// engine, tier, mix, value size — one readable, unique name per cell.
func (c Cell) DirName() string {
	return fmt.Sprintf("%s-%s-%s-%dB", c.Engine, c.Tier, c.Mix, c.ValueSize)
}

// TierDeclaration states one engine's standing under one durability
// tier: the configuration sentence reports print when the tier is
// aligned, and the reason no numbers exist when it is not.
type TierDeclaration struct {
	Tier          SyncTier
	Aligned       bool
	Configuration string
}

// EngineAlignment declares one engine's standing under every tier of
// the matrix — the engine-by-engine durability declaration the report
// carries with its numbers.
type EngineAlignment struct {
	Engine string
	Tiers  []TierDeclaration
}

// CellResult is one produced cell: the numbers of one driver's run of
// one workload.
type CellResult struct {
	Cell   Cell
	Result Result
}

// CellFailure is one cell that produced no numbers, with the reason —
// recorded per cell so the surviving cells' numbers still stand and
// the gap is visible, never silent.
type CellFailure struct {
	Cell Cell
	Err  error
}

// MatrixResult is the matrix's whole output: the durability
// declarations, the produced cells' numbers, and the recorded failures.
type MatrixResult struct {
	Alignment []EngineAlignment
	Cells     []CellResult
	Failures  []CellFailure
}

// DriverFactory builds one driver for an engine under a durability
// tier — the seam every engine is driven through. Production passes
// ForTier.
type DriverFactory func(name string, tier SyncTier, period time.Duration) (Driver, error)

// PlanMatrix enumerates the spec's runnable cells and its per-engine
// durability declarations: every engine×mix×size combination at every
// tier the engine supports — unsupported tiers contribute no cells and
// say so in the declaration. Cells order by engine, per-write tier
// ahead of group-commit, then mix, then value size. An invalid spec is
// an error before any cell runs.
func PlanMatrix(spec MatrixSpec) ([]Cell, []EngineAlignment, error) {
	if err := spec.validate(); err != nil {
		return nil, nil, err
	}
	cells := make([]Cell, 0, len(spec.Engines)*len(spec.Mixes)*len(spec.ValueSizes))
	alignment := make([]EngineAlignment, 0, len(spec.Engines))
	for _, engine := range spec.Engines {
		entry := EngineAlignment{Engine: engine}
		for _, tier := range matrixSyncTiers {
			td := tierDeclaration(engine, tier, spec.SyncPeriod)
			entry.Tiers = append(entry.Tiers, td)
			if !td.Aligned {
				continue
			}
			for _, mix := range spec.Mixes {
				for _, size := range spec.ValueSizes {
					cells = append(cells, Cell{Engine: engine, Tier: tier, Mix: mix, ValueSize: size})
				}
			}
		}
		alignment = append(alignment, entry)
	}
	return cells, alignment, nil
}

// validate rejects a spec no cell could run: unknown engines, empty
// axes, non-positive steps, a missing zipf theta, and a missing
// group-commit period exactly when some engine runs that tier.
func (s MatrixSpec) validate() error {
	if len(s.Engines) == 0 {
		return errors.New("matrix: no engines")
	}
	needsPeriod := false
	for _, engine := range s.Engines {
		if _, err := ForTier(engine, SyncPerWrite, s.SyncPeriod); err != nil {
			return fmt.Errorf("matrix: %w", err)
		}
		if tierAligned(engine, SyncGroupCommit, time.Nanosecond) {
			needsPeriod = true
		}
	}
	if len(s.Mixes) == 0 {
		return errors.New("matrix: no mixes")
	}
	for _, mix := range s.Mixes {
		if mix < YCSBA || mix > YCSBC {
			return fmt.Errorf("matrix: unknown mix %s", mix)
		}
	}
	if len(s.ValueSizes) == 0 {
		return errors.New("matrix: no value sizes")
	}
	for _, size := range s.ValueSizes {
		if size <= 0 {
			return fmt.Errorf("matrix: value size %d must be positive", size)
		}
	}
	if s.Keys == 0 {
		return errors.New("matrix: keys must be positive")
	}
	if s.RunOps <= 0 {
		return fmt.Errorf("matrix: run ops %d must be positive", s.RunOps)
	}
	switch s.Dist {
	case KeyUniform:
	case KeyZipf:
		if s.ZipfTheta <= 0 {
			return fmt.Errorf("matrix: zipf theta %g must be positive", s.ZipfTheta)
		}
	default:
		return fmt.Errorf("matrix: unknown key distribution %d", int(s.Dist))
	}
	if needsPeriod && s.SyncPeriod <= 0 {
		return fmt.Errorf("matrix: group-commit period %d must be positive", s.SyncPeriod)
	}
	return nil
}

// tierAligned reports whether the engine runs the tier, by asking the
// registry — the one place support is defined, so the plan can never
// drift from what ForTier builds.
func tierAligned(engine string, tier SyncTier, period time.Duration) bool {
	_, err := ForTier(engine, tier, period)
	return err == nil
}

// tierDeclaration states one engine×tier standing, the sentence the
// reports print with the numbers.
func tierDeclaration(engine string, tier SyncTier, period time.Duration) TierDeclaration {
	td := TierDeclaration{Tier: tier, Aligned: tierAligned(engine, tier, period)}
	switch engine {
	case "elena":
		if tier == SyncPerWrite {
			td.Configuration = "every append fdatasync'ed to the data file before it acknowledges (elena.WithSyncAlways, the store default)"
		} else {
			td.Configuration = fmt.Sprintf("appends acknowledge after the write; a background flusher fdatasyncs the active data file once per %s (elena.WithSyncInterval; the final flush is forced at close and at every rotation)", period)
		}
	case "bbolt":
		if tier == SyncPerWrite {
			td.Configuration = "every append commits as its own write transaction under bbolt's default sync-on-commit (Options.NoSync left false)"
		} else {
			td.Configuration = "bbolt exposes no periodic-sync configuration (NoSync only turns syncing off), so this tier produces no bbolt numbers"
		}
	case "badger":
		if tier == SyncPerWrite {
			td.Configuration = "SyncWrites set to true: badger msyncs the write-ahead value log after every write, the tier its option documentation scopes as surviving hard reboots (the v4 default is false; set explicitly)"
		} else {
			td.Configuration = "badger exposes no periodic-sync configuration (SyncWrites false drops to plain mmap writes), so this tier produces no badger numbers"
		}
	case "rosedb":
		if tier == SyncPerWrite {
			td.Configuration = "every append runs as one batch with Sync=true, whose commit fsyncs the write-ahead log before it acknowledges (the DB-level Sync flag stays false — in rosedb v2 it governs a write-ahead path the batch operations never ride, and it disables the batch-level sync)"
		} else {
			td.Configuration = "rosedb's BytesPerSync flushes by accumulated bytes rather than by a time period, which is not this tier, so it produces no rosedb numbers"
		}
	}
	return td
}

// RunMatrix runs every planned cell of the spec through the registry's
// drivers, each in its own fresh directory under root (created when
// missing). A cell that fails is recorded in Failures and the matrix
// keeps running; the returned result always carries the durability
// declarations.
func RunMatrix(spec MatrixSpec, root string) (MatrixResult, error) {
	return runMatrix(spec, root, ForTier)
}

// runMatrix is RunMatrix over an injected driver factory — the
// orchestration seam the tests drive with recording drivers.
func runMatrix(spec MatrixSpec, root string, newDriver DriverFactory) (MatrixResult, error) {
	cells, alignment, err := PlanMatrix(spec)
	if err != nil {
		return MatrixResult{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return MatrixResult{}, fmt.Errorf("create matrix root %s: %w", root, err)
	}
	res := MatrixResult{Alignment: alignment}
	workloads := make(map[workloadKey]Workload)
	for _, cell := range cells {
		res.Cells, res.Failures = runCell(res.Cells, res.Failures, spec, cell, root, workloads, newDriver)
	}
	return res, nil
}

// runCell runs one cell and folds its outcome into the result slices.
func runCell(cells []CellResult, failures []CellFailure, spec MatrixSpec, cell Cell, root string, workloads map[workloadKey]Workload, newDriver DriverFactory) ([]CellResult, []CellFailure) {
	fail := func(err error) ([]CellResult, []CellFailure) {
		return cells, append(failures, CellFailure{Cell: cell, Err: err})
	}
	w, err := workloadFor(workloads, spec, cell)
	if err != nil {
		return fail(err)
	}
	driver, err := newDriver(cell.Engine, cell.Tier, spec.SyncPeriod)
	if err != nil {
		return fail(err)
	}
	dir := filepath.Join(root, cell.DirName())
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fail(fmt.Errorf("fresh dir %s: %w", dir, err))
	}
	run, err := Run(driver, dir, w)
	if err != nil {
		return fail(err)
	}
	return append(cells, CellResult{Cell: cell, Result: run}), failures
}

// workloadKey identifies one workload of the matrix: mix and value
// size — tier and engine never enter, one workload per key drives them
// all.
type workloadKey struct {
	mix  YCSB
	size int
}

// workloadFor returns the workload of one mix and value size,
// generating it once and reusing the identical sequence for every
// engine and tier.
func workloadFor(workloads map[workloadKey]Workload, spec MatrixSpec, cell Cell) (Workload, error) {
	key := workloadKey{mix: cell.Mix, size: cell.ValueSize}
	if w, ok := workloads[key]; ok {
		return w, nil
	}
	w, err := YCSBWorkload(YCSBSpec{
		Mix:       cell.Mix,
		Keys:      spec.Keys,
		Dist:      spec.Dist,
		ZipfTheta: spec.ZipfTheta,
		RunOps:    spec.RunOps,
		ValueSize: cell.ValueSize,
		Seed:      spec.Seed,
	})
	if err != nil {
		return Workload{}, err
	}
	workloads[key] = w
	return w, nil
}

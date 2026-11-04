// Command matrix is the YCSB comparison runner: it drives engines ×
// mixes × value sizes at aligned durability tiers — one command, one
// seed, one report per run — and writes the raw numbers and the
// declarations they travel with. Every engine executes the identical
// operation sequence the seed selects; combinations whose durability
// does not align produce no numbers and say so in the report.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"elena/bench"
)

// defaults of the declared matrix: YCSB's zipfian key distribution and
// mix set, the five value-size steps, a ten-millisecond group-commit
// period. Operation counts stay caller-declared — a full run's scale
// belongs to the environment it runs in, not to a default.
const (
	defaultKeys       = 10000
	defaultRunOps     = 10000
	defaultSyncPeriod = 10 * time.Millisecond
	defaultZipfTheta  = 0.99
)

// usageText is the public help surface. Every line leads with a system
// signal.
const usageText = `usage: matrix [arguments]

runs the YCSB A/B/C comparison matrix: engines x mixes x value sizes at aligned durability tiers

arguments:
  --root PATH            fresh root for this run's cell directories (required, must not exist)
  --engines a,b,c        engines to drive (default: elena,bbolt,badger,rosedb)
  --mixes a,b,c          YCSB mixes (default: a,b,c)
  --values B1,B2,...     value sizes in bytes (default: 64,256,1024,4096,16384)
  --keys N               key space size (default 10000)
  --run-ops N            operations per run phase (default 10000)
  --seed N               workload seed (default 1)
  --dist uniform|zipf    key distribution (default zipf)
  --zipf-theta F         zipf theta (default 0.99)
  --sync-period D        group-commit period (default 10ms)
  --json-out PATH        write the raw JSON report here (optional)
  --md-out PATH          write the markdown report here (optional)

output: one line per produced cell with each phase's throughput and P99; engines without an aligned durability tier produce no numbers for it, declared in the reports
`

// usageError marks argument problems: Run prints the message and exits
// with code 2 instead of 1, separating "wrong ask" from "run failure".
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// Run parses one command line — args without the program name —
// against the injected writers and returns the process exit code: 0 on
// a completed run, 2 on a usage problem, 1 on a run failure (including
// failed cells).
func Run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		_, _ = fmt.Fprint(stderr, usageText)
		return 2
	}
	err = run(cfg, stdout)
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "matrix: %v\n", err)
	return 1
}

// config is one parsed command line.
type config struct {
	root       string
	engines    []string
	mixes      []bench.YCSB
	values     []int
	keys       uint64
	runOps     int
	seed       uint64
	dist       bench.KeyDist
	zipfTheta  float64
	syncPeriod time.Duration
	jsonOut    string
	mdOut      string
}

// parse turns the command line into a validated config: subset lists
// resolved, numbers positive, distributions known.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	var engines, mixes, values, dist string
	var theta float64
	fs.StringVar(&cfg.root, "root", "", "fresh root for this run's cell directories")
	fs.StringVar(&engines, "engines", strings.Join(bench.Drivers(), ","), "engines to drive")
	fs.StringVar(&mixes, "mixes", "a,b,c", "YCSB mixes")
	fs.StringVar(&values, "values", "64,256,1024,4096,16384", "value sizes in bytes")
	fs.Uint64Var(&cfg.keys, "keys", defaultKeys, "key space size")
	fs.IntVar(&cfg.runOps, "run-ops", defaultRunOps, "operations per run phase")
	fs.Uint64Var(&cfg.seed, "seed", 1, "workload seed")
	fs.StringVar(&dist, "dist", "zipf", "key distribution: uniform or zipf")
	fs.Float64Var(&theta, "zipf-theta", defaultZipfTheta, "zipf theta")
	fs.DurationVar(&cfg.syncPeriod, "sync-period", defaultSyncPeriod, "group-commit period")
	fs.StringVar(&cfg.jsonOut, "json-out", "", "write the raw JSON report here")
	fs.StringVar(&cfg.mdOut, "md-out", "", "write the markdown report here")
	if err := fs.Parse(args); err != nil {
		return config{}, usagef("arguments: %v", err)
	}
	if fs.NArg() > 0 {
		return config{}, usagef("unexpected arguments: %v", fs.Args())
	}
	if cfg.root == "" {
		return config{}, usagef("--root is required")
	}
	var err error
	if cfg.engines, err = parseEngines(engines); err != nil {
		return config{}, err
	}
	if cfg.mixes, err = parseMixes(mixes); err != nil {
		return config{}, err
	}
	if cfg.values, err = parseValues(values); err != nil {
		return config{}, err
	}
	if cfg.dist, err = parseDist(dist, theta); err != nil {
		return config{}, err
	}
	cfg.zipfTheta = theta
	if cfg.keys == 0 {
		return config{}, usagef("--keys must be positive")
	}
	if cfg.runOps <= 0 {
		return config{}, usagef("--run-ops %d must be positive", cfg.runOps)
	}
	if cfg.seed == 0 {
		return config{}, usagef("--seed must be positive")
	}
	if cfg.syncPeriod <= 0 {
		return config{}, usagef("--sync-period must be positive")
	}
	return cfg, nil
}

// parseEngines resolves the engine list against the registry; every
// name must be known and the list non-empty.
func parseEngines(raw string) ([]string, error) {
	if raw == "" {
		return nil, usagef("--engines must name at least one engine")
	}
	known := map[string]bool{}
	for _, name := range bench.Drivers() {
		known[name] = true
	}
	names := strings.Split(raw, ",")
	for _, name := range names {
		if !known[name] {
			return nil, usagef("--engines: unknown engine %q", name)
		}
	}
	return names, nil
}

// parseMixes resolves the mix list onto the YCSB mixes; every letter
// must be a known mix and the list non-empty.
func parseMixes(raw string) ([]bench.YCSB, error) {
	if raw == "" {
		return nil, usagef("--mixes must name at least one mix")
	}
	letters := map[string]bench.YCSB{"a": bench.YCSBA, "b": bench.YCSBB, "c": bench.YCSBC}
	var mixes []bench.YCSB
	for _, part := range strings.Split(raw, ",") {
		mix, ok := letters[part]
		if !ok {
			return nil, usagef("--mixes: unknown mix %q (want a, b, or c)", part)
		}
		mixes = append(mixes, mix)
	}
	return mixes, nil
}

// parseValues resolves the value-size list; every entry must parse as
// a positive byte count and the list must be non-empty.
func parseValues(raw string) ([]int, error) {
	if raw == "" {
		return nil, usagef("--values must name at least one value size")
	}
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		size, err := strconv.Atoi(part)
		if err != nil {
			return nil, usagef("--values: %q is not a number", part)
		}
		if size <= 0 {
			return nil, usagef("--values: %d must be positive", size)
		}
		values = append(values, size)
	}
	return values, nil
}

// parseDist resolves the distribution and its theta: zipf requires a
// positive theta, uniform ignores it.
func parseDist(raw string, theta float64) (bench.KeyDist, error) {
	switch raw {
	case "uniform":
		return bench.KeyUniform, nil
	case "zipf":
		if theta <= 0 {
			return 0, usagef("--zipf-theta %g must be positive", theta)
		}
		return bench.KeyZipf, nil
	default:
		return 0, usagef("--dist: unknown distribution %q (want uniform or zipf)", raw)
	}
}

// run runs the matrix and writes the report lines and files. A failed
// cell does not stop the matrix; every failure joins the returned
// error after the surviving cells' lines are out.
func run(cfg config, stdout io.Writer) error {
	if _, err := os.Stat(cfg.root); err == nil {
		return fmt.Errorf("root %s exists: a matrix run needs a fresh root", cfg.root)
	}
	for _, path := range []string{cfg.jsonOut, cfg.mdOut} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("report path %s exists: a report file is one run's artifact", path)
		}
	}

	res, err := bench.RunMatrix(cfg.spec(), cfg.root)
	if err != nil {
		return err
	}
	for _, cr := range res.Cells {
		if err := writef(stdout, "cell %s: %s\n", cr.Cell.DirName(), phaseLine(cr.Result.Phases)); err != nil {
			return err
		}
	}
	if err := writeReports(cfg, res); err != nil {
		return err
	}
	if err := writef(stdout, "matrix: %d cells produced, %d failures\n", len(res.Cells), len(res.Failures)); err != nil {
		return err
	}
	if len(res.Failures) > 0 {
		errs := make([]error, 0, len(res.Failures))
		for _, f := range res.Failures {
			errs = append(errs, fmt.Errorf("cell %s: %w", f.Cell.DirName(), f.Err))
		}
		return errors.Join(errs...)
	}
	return nil
}

// spec is the parsed config as the matrix's declaration.
func (c config) spec() bench.MatrixSpec {
	return bench.MatrixSpec{
		Engines:    c.engines,
		Mixes:      c.mixes,
		ValueSizes: c.values,
		Keys:       c.keys,
		Dist:       c.dist,
		ZipfTheta:  c.zipfTheta,
		RunOps:     c.runOps,
		Seed:       c.seed,
		SyncPeriod: c.syncPeriod,
	}
}

// phaseLine summarizes one cell's phases: throughput and P99 each, the
// tail latency the reports lead with.
func phaseLine(phases []bench.PhaseResult) string {
	parts := make([]string, 0, len(phases))
	for _, pr := range phases {
		parts = append(parts, fmt.Sprintf("%s %.1f ops/sec p99 %v",
			pr.Phase, pr.Throughput, pr.Latency.P99.Round(time.Microsecond)))
	}
	return strings.Join(parts, "; ")
}

// writeReports renders the two report forms to their declared paths.
func writeReports(cfg config, res bench.MatrixResult) error {
	if cfg.jsonOut != "" {
		doc, err := bench.RenderMatrixJSON(res)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cfg.jsonOut, doc, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfg.jsonOut, err)
		}
	}
	if cfg.mdOut != "" {
		if err := os.WriteFile(cfg.mdOut, []byte(bench.RenderMatrixMarkdown(res)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfg.mdOut, err)
		}
	}
	return nil
}

// writef formats one report line.
func writef(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	return err
}

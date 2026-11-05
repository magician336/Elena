// Command recovery is the rig's CLI: it runs the kill -9 recovery
// measurement over data-size tiers — a child process appends the
// tier's dataset through the public library API, the parent kills it
// once the schedule is acknowledged, and the reopen is timed — and
// prints each run's segments together with the declared timing
// convention. Repeats of one tier must agree within one order of
// magnitude; a tier that does not fails the run.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/magician336/elena/bench/recovery"
)

// usageText is the public help surface. Every line leads with a system
// signal.
const usageText = `usage: recovery [arguments]

runs the kill -9 recovery rig over data-size tiers: a child appends through the public
library API, the parent observes every acknowledgment and sends SIGKILL, the reopen is timed

arguments:
  --tier LIST       comma-separated tiers: micro,256mb,1gb,4gb (default micro)
  --runs N          runs per tier, compared for stability (default 2)
  --value-size B    override each tier's value size; must divide the tier target
  --seed N          dataset seed (default 1)
  --dir PATH        fresh root directory for the per-run stores (required, must not exist)

timing convention: kill->open counts SIGKILL delivery through child teardown and reap to
  Open returning; open->first-read counts Open returning to the first acknowledged event
  read back byte-exact; kill->first-read is their sum
durability: the writer rides the store's default write-synchronized regime; an
  acknowledged event is durable the moment the parent observes its line
stability: repeats of one tier must agree within one order of magnitude
`

// timingConventionLine is the one-line form of the convention printed
// above every report, so a copied line carries its own definition.
const timingConventionLine = "timing convention: kill->open counts SIGKILL delivery through child teardown and reap to Open returning; open->first-read counts Open returning to the first acknowledged event read back byte-exact; kill->first-read is their sum"

// usageError marks argument problems: Run prints the message and exits
// with code 2 instead of 1, separating "wrong ask" from "run failure".
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef builds a usageError.
func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// measureFunc is the rig entry the CLI drives; tests inject a fake.
type measureFunc func(recovery.Spec) (recovery.Result, error)

// config is one parsed command line.
type config struct {
	tiers     []string
	runs      int
	seed      int64
	valueSize int // 0: each tier's default
	dir       string
}

// Run parses one command line — args without the program name —,
// drives the rig through measure, writes the report to stdout and
// problems to stderr, and returns the process exit code: 0 on a
// completed run, 2 on a usage problem, 1 on a run failure — a failed
// measurement or an unstable tier.
func Run(args []string, stdout, stderr io.Writer, measure measureFunc) int {
	cfg, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		_, _ = fmt.Fprint(stderr, usageText)
		return 2
	}
	err = run(cfg, stdout, stderr, measure)
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "recovery: %v\n", err)
	return 1
}

// run drives the tiers and writes the report lines.
func run(cfg config, stdout, stderr io.Writer, measure measureFunc) error {
	if err := os.Mkdir(cfg.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", cfg.dir, err)
	}
	if err := writef(stdout, "%s\n", timingConventionLine); err != nil {
		return err
	}
	for _, name := range cfg.tiers {
		tier, err := recovery.TierByName(name)
		if err != nil {
			return err // parse validated; the table shrank underneath us
		}
		if cfg.valueSize > 0 {
			tier.ValueSize = cfg.valueSize
		}
		totals := make([]time.Duration, 0, cfg.runs)
		for run := 1; run <= cfg.runs; run++ {
			spec := tier.Spec(filepath.Join(cfg.dir, tier.Name+"-run"+strconv.Itoa(run)), cfg.seed, 0)
			res, err := measure(spec)
			if err != nil {
				return fmt.Errorf("tier %s run %d: %w", tier.Name, run, err)
			}
			if err := renderRun(stdout, tier.Name, run, cfg.runs, res); err != nil {
				return err
			}
			totals = append(totals, res.KillToFirstRead)
		}
		if cfg.runs > 1 {
			stable, err := renderStability(stdout, tier.Name, totals)
			if err != nil {
				return err
			}
			if !stable {
				return fmt.Errorf("tier %s: repeated runs differ by more than one order of magnitude", tier.Name)
			}
		}
	}
	return nil
}

// renderRun prints one measured run: the tier's dataset shape and the
// three timing segments, rounded to microseconds for the report.
func renderRun(w io.Writer, tier string, run, runs int, res recovery.Result) error {
	return writef(w, "tier %s (%d bytes = %d events x %dB) run %d/%d: acked %d, kill->open %v, open->first-read %v, kill->first-read %v\n",
		tier, res.TargetBytes, res.Events, res.PayloadSize, run, runs, res.AckedEvents,
		res.KillToOpen.Round(time.Microsecond), res.OpenToFirstRead.Round(time.Microsecond),
		res.KillToFirstRead.Round(time.Microsecond))
}

// renderStability prints one tier's stability verdict over its totals —
// fastest against slowest, their ratio, and whether they agree within
// one order of magnitude — and reports the verdict as its bool.
func renderStability(w io.Writer, tier string, totals []time.Duration) (bool, error) {
	ordered := slices.Clone(totals)
	slices.Sort(ordered)
	fastest, slowest := ordered[0], ordered[len(ordered)-1]
	stable := recovery.SameMagnitude(fastest, slowest)
	ratio := "n/a"
	if fastest > 0 {
		ratio = fmt.Sprintf("%.2f", float64(slowest)/float64(fastest))
	}
	verdict := "within one order of magnitude"
	if !stable {
		verdict = "NOT within one order of magnitude"
	}
	return stable, writef(w, "tier %s stability: kill->first-read %v vs %v, ratio %s — %s\n",
		tier, fastest.Round(time.Microsecond), slowest.Round(time.Microsecond), ratio, verdict)
}

// parse turns the command line into a validated config: every tier
// name must be known, runs positive, the value size positive when
// given and dividing every selected tier's target exactly.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("recovery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	var tiers string
	fs.StringVar(&tiers, "tier", "micro", "comma-separated tiers to measure")
	fs.IntVar(&cfg.runs, "runs", 2, "runs per tier")
	fs.Int64Var(&cfg.seed, "seed", 1, "dataset seed")
	fs.IntVar(&cfg.valueSize, "value-size", 0, "override the tier value size; 0 keeps the tier default")
	fs.StringVar(&cfg.dir, "dir", "", "fresh root directory for the per-run stores")
	if err := fs.Parse(args); err != nil {
		return config{}, usagef("arguments: %v", err)
	}
	if fs.NArg() > 0 {
		return config{}, usagef("unexpected arguments: %v", fs.Args())
	}
	if cfg.dir == "" {
		return config{}, usagef("--dir is required")
	}
	if cfg.runs < 1 {
		return config{}, usagef("--runs %d must be at least 1", cfg.runs)
	}
	if cfg.valueSize < 0 {
		return config{}, usagef("--value-size %d must be positive when given", cfg.valueSize)
	}
	cfg.tiers = []string{}
	for _, name := range strings.Split(tiers, ",") {
		tier, err := recovery.TierByName(name)
		if err != nil {
			return config{}, usagef("%v", err)
		}
		if cfg.valueSize > 0 && tier.TargetBytes%int64(cfg.valueSize) != 0 {
			return config{}, usagef("--value-size %d does not divide tier %s's %d-byte target exactly",
				cfg.valueSize, tier.Name, tier.TargetBytes)
		}
		cfg.tiers = append(cfg.tiers, tier.Name)
	}
	return cfg, nil
}

// writef formats one report line.
func writef(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	return err
}

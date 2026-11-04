package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"elena/bench"
)

// usageText is the public help surface. Every line leads with a system
// signal.
const usageText = `usage: focused [arguments]

runs one focused-metric scenario: latency across one explicit merge, paired startup
timings with and without hint files, per-entry index memory, or write amplification

arguments:
  --scenario merge-curve|startup-pair|index-memory|write-amp   scenario to run (required)
  --dir PATH             fresh store directory for this run (required, must not exist)
  --sessions N           trajectories in the dataset (default 4096)
  --events N             events appended per trajectory (default 16)
  --value-size B         payload bytes per event (default 1024)
  --warmup-ops N         operations before the merge starts, merge-curve only (default 100000)
  --window D             curve window width, merge-curve only (default 500ms)
  --context-windows N    windows before the merge start, merge-curve only (default 6)
  --tail-windows N       windows past the merge end, merge-curve only (default 12)
  --json-out PATH        write the raw JSON report here (optional)

output: the scenario's figure lines, then the curve table for merge-curve; the JSON report carries the same numbers
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
// a completed run, 2 on a usage problem, 1 on a run failure.
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
	_, _ = fmt.Fprintf(stderr, "focused: %v\n", err)
	return 1
}

// config is one parsed command line. Fields outside the chosen
// scenario's needs keep their defaults; only the declared scenario
// runs.
type config struct {
	scenario       string
	dir            string
	sessions       int
	events         int
	valueSize      int
	warmupOps      int
	window         time.Duration
	contextWindows int
	tailWindows    int
	jsonOut        string
}

// parse turns the command line into a validated config.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("focused", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	fs.StringVar(&cfg.scenario, "scenario", "", "scenario to run: merge-curve, startup-pair, index-memory, or write-amp")
	fs.StringVar(&cfg.dir, "dir", "", "fresh store directory for this run")
	fs.IntVar(&cfg.sessions, "sessions", 4096, "trajectories in the dataset")
	fs.IntVar(&cfg.events, "events", 16, "events appended per trajectory")
	fs.IntVar(&cfg.valueSize, "value-size", 1024, "payload bytes per event")
	fs.IntVar(&cfg.warmupOps, "warmup-ops", 100000, "operations before the merge starts")
	fs.DurationVar(&cfg.window, "window", 500*time.Millisecond, "curve window width")
	fs.IntVar(&cfg.contextWindows, "context-windows", 6, "windows before the merge start")
	fs.IntVar(&cfg.tailWindows, "tail-windows", 12, "windows past the merge end")
	fs.StringVar(&cfg.jsonOut, "json-out", "", "write the raw JSON report here")
	if err := fs.Parse(args); err != nil {
		return config{}, usagef("arguments: %v", err)
	}
	if fs.NArg() > 0 {
		return config{}, usagef("unexpected arguments: %v", fs.Args())
	}
	switch cfg.scenario {
	case "merge-curve", "startup-pair", "index-memory", "write-amp":
	case "":
		return config{}, usagef("--scenario is required: merge-curve, startup-pair, index-memory, or write-amp")
	default:
		return config{}, usagef("--scenario: unknown scenario %q", cfg.scenario)
	}
	if cfg.dir == "" {
		return config{}, usagef("--dir is required")
	}
	if _, err := os.Stat(cfg.dir); err == nil {
		return config{}, usagef("--dir %s exists: a scenario run needs a fresh directory", cfg.dir)
	}
	if cfg.sessions <= 0 {
		return config{}, usagef("--sessions %d must be positive", cfg.sessions)
	}
	if cfg.events <= 0 {
		return config{}, usagef("--events %d must be positive", cfg.events)
	}
	if cfg.valueSize <= 0 {
		return config{}, usagef("--value-size %d must be positive", cfg.valueSize)
	}
	if cfg.scenario == "merge-curve" {
		if cfg.warmupOps <= 0 {
			return config{}, usagef("--warmup-ops %d must be positive", cfg.warmupOps)
		}
		if cfg.window <= 0 {
			return config{}, usagef("--window %s must be positive", cfg.window)
		}
		if cfg.contextWindows < 0 {
			return config{}, usagef("--context-windows %d must not be negative", cfg.contextWindows)
		}
		if cfg.tailWindows <= 0 {
			return config{}, usagef("--tail-windows %d must be positive", cfg.tailWindows)
		}
	}
	if cfg.jsonOut != "" {
		if _, err := os.Stat(cfg.jsonOut); err == nil {
			return config{}, usagef("--json-out %s exists: a report file is one run's artifact", cfg.jsonOut)
		}
	}
	return cfg, nil
}

// run drives the declared scenario, prints its figure lines, and
// writes the optional raw JSON report.
func run(cfg config, stdout io.Writer) error {
	var report []byte
	var err error
	switch cfg.scenario {
	case "merge-curve":
		report, err = runMergeCurve(cfg, stdout)
	case "startup-pair":
		report, err = runStartupPair(cfg, stdout)
	case "index-memory":
		report, err = runIndexMemory(cfg, stdout)
	case "write-amp":
		report, err = runWriteAmplification(cfg, stdout)
	}
	if err != nil {
		return err
	}
	if cfg.jsonOut != "" {
		if err := os.WriteFile(cfg.jsonOut, report, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfg.jsonOut, err)
		}
	}
	return nil
}

// docOf renders one scenario result into its raw JSON document: the
// scenario name beside the numbers, fixed field order by struct.
func docOf(scenario string, result any) ([]byte, error) {
	out, err := json.MarshalIndent(struct {
		Scenario string `json:"scenario"`
		Result   any    `json:"result"`
	}{Scenario: scenario, Result: result}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render json: %w", err)
	}
	return append(out, '\n'), nil
}

// runMergeCurve drives the merge-curve scenario and prints the figure
// lines plus one row per sampled window.
func runMergeCurve(cfg config, stdout io.Writer) ([]byte, error) {
	res, err := bench.RunMergeCurve(cfg.dir, bench.MergeCurveConfig{
		Sessions:       cfg.sessions,
		ValueSize:      cfg.valueSize,
		WarmupOps:      cfg.warmupOps,
		Window:         cfg.window,
		ContextWindows: cfg.contextWindows,
		TailWindows:    cfg.tailWindows,
	})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(stdout, "merge-curve: merge wall %s over %d appends and %d reads observed\n",
		time.Duration(res.MergeElapsedNS).Round(time.Microsecond), res.ObservedAppends, res.ObservedReads); err != nil {
		return nil, err
	}
	window := time.Duration(res.WindowNS)
	for _, p := range res.Points {
		if _, err := fmt.Fprintf(stdout, "window %+v: appends %d p50 %v p99 %v; reads %d p50 %v p99 %v\n",
			time.Duration(p.OffsetNS).Round(window),
			p.Appends,
			time.Duration(p.AppendP50NS).Round(time.Microsecond),
			time.Duration(p.AppendP99NS).Round(time.Microsecond),
			p.Reads,
			time.Duration(p.ReadP50NS).Round(time.Microsecond),
			time.Duration(p.ReadP99NS).Round(time.Microsecond)); err != nil {
			return nil, err
		}
	}
	return docOf(cfg.scenario, res)
}

// runStartupPair drives the paired startup scenario and prints both
// timings against the dataset facts that make them one comparison.
func runStartupPair(cfg config, stdout io.Writer) ([]byte, error) {
	res, err := bench.RunStartupPair(cfg.dir, bench.StartupPairConfig{
		Sessions:         cfg.sessions,
		EventsPerSession: cfg.events,
		ValueSize:        cfg.valueSize,
	})
	if err != nil {
		return nil, err
	}
	for _, t := range []struct {
		mode   string
		timing bench.StartupTiming
	}{{res.Hint.Mode, res.Hint}, {res.NoHint.Mode, res.NoHint}} {
		if _, err := fmt.Fprintf(stdout, "startup %s: open %s first-read %s over %d live entries %d data bytes\n",
			t.mode, time.Duration(t.timing.OpenNS).Round(time.Microsecond),
			time.Duration(t.timing.FirstReadNS).Round(time.Microsecond),
			t.timing.LiveEntries, t.timing.DataBytes); err != nil {
			return nil, err
		}
	}
	return docOf(cfg.scenario, res)
}

// runIndexMemory drives the per-entry index memory scenario and prints
// the figure beside the vendor-reported reference it is read against.
func runIndexMemory(cfg config, stdout io.Writer) ([]byte, error) {
	res, err := bench.RunIndexMemory(cfg.dir, bench.IndexMemoryConfig{
		Sessions:         cfg.sessions,
		EventsPerSession: cfg.events,
		ValueSize:        cfg.valueSize,
	})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(stdout, "index-memory: %.1f bytes/entry over %d live entries (%d index bytes)\n",
		res.BytesPerEntry, res.LiveEntries, res.IndexMemoryBytes); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(stdout, "index-memory: reference %s\n", bench.NutsDBIndexMemoryReference); err != nil {
		return nil, err
	}
	return docOf(cfg.scenario, res)
}

// runWriteAmplification drives the write-amplification scenario and
// prints the byte pair before and after one explicit merge with the
// accounting declaration.
func runWriteAmplification(cfg config, stdout io.Writer) ([]byte, error) {
	res, err := bench.RunWriteAmplification(cfg.dir, bench.WriteAmplificationConfig{
		Sessions:         cfg.sessions,
		EventsPerSession: cfg.events,
		ValueSize:        cfg.valueSize,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range []struct {
		when    string
		reading bench.WriteAmplificationReading
	}{{"before merge", res.BeforeMerge}, {"after merge", res.AfterMerge}} {
		if _, err := fmt.Fprintf(stdout, "write-amp %s: logical %d flushed %d ratio %.2f\n",
			r.when, r.reading.LogicalBytes, r.reading.FlushedBytes, r.reading.Ratio); err != nil {
			return nil, err
		}
	}
	if _, err := fmt.Fprintf(stdout, "write-amp: merge flushed %d bytes; %s\n",
		res.MergeFlushedBytes, bench.WriteAmplificationBasis); err != nil {
		return nil, err
	}
	return docOf(cfg.scenario, res)
}

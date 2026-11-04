package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"elena/bench"
)

// usageText is the public help surface. Every line leads with a system
// signal.
const usageText = `usage: scenarios [arguments]

runs one elena scenario load through the public library API: pure append, append plus
point-in-time reads, or append plus TTL expiry with the sweep-merge reclamation chain

arguments:
  --scenario append|point|ttl   scenario to run (required)
  --dir PATH          fresh store directory for this run (required, must not exist)
  --sessions N        trajectories in the load (default 64)
  --events N          events appended per trajectory per generation (default 1000)
  --reads N           point-in-time reads per trajectory, point scenario only (default 1000)
  --value-size B      payload bytes per event (default 256)
  --seed N            operation-plan seed, point scenario only (default 7)
  --ttl D             session lifetime, ttl scenario only (default 30s)
  --sweep-period D    background sweep period, ttl scenario only (default 5s)
  --generations N     timed session generations, ttl scenario only (default 4)
  --json-out PATH     write the raw JSON report here (optional)

output: one line per phase with throughput and the P50/P99/P999 trio; the JSON report carries the same numbers
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
	_, _ = fmt.Fprintf(stderr, "scenarios: %v\n", err)
	return 1
}

// config is one parsed command line. Fields outside the chosen
// scenario's needs keep their defaults; only the declared scenario
// runs.
type config struct {
	scenario    string
	dir         string
	sessions    int
	events      int
	reads       int
	valueSize   int
	seed        uint64
	ttl         time.Duration
	sweepPeriod time.Duration
	generations int
	jsonOut     string
}

// parse turns the command line into a validated config.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("scenarios", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	fs.StringVar(&cfg.scenario, "scenario", "", "scenario to run: append, point, or ttl")
	fs.StringVar(&cfg.dir, "dir", "", "fresh store directory for this run")
	fs.IntVar(&cfg.sessions, "sessions", 64, "trajectories in the load")
	fs.IntVar(&cfg.events, "events", 1000, "events per trajectory per generation")
	fs.IntVar(&cfg.reads, "reads", 1000, "point-in-time reads per trajectory")
	fs.IntVar(&cfg.valueSize, "value-size", 256, "payload bytes per event")
	fs.Uint64Var(&cfg.seed, "seed", 7, "operation-plan seed")
	fs.DurationVar(&cfg.ttl, "ttl", 30*time.Second, "session lifetime")
	fs.DurationVar(&cfg.sweepPeriod, "sweep-period", 5*time.Second, "background sweep period")
	fs.IntVar(&cfg.generations, "generations", 4, "timed session generations")
	fs.StringVar(&cfg.jsonOut, "json-out", "", "write the raw JSON report here")
	if err := fs.Parse(args); err != nil {
		return config{}, usagef("arguments: %v", err)
	}
	if fs.NArg() > 0 {
		return config{}, usagef("unexpected arguments: %v", fs.Args())
	}
	switch cfg.scenario {
	case "append", "point", "ttl":
	case "":
		return config{}, usagef("--scenario is required: append, point, or ttl")
	default:
		return config{}, usagef("--scenario: unknown scenario %q (want append, point, or ttl)", cfg.scenario)
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
	if cfg.scenario == "point" && cfg.reads < 0 {
		return config{}, usagef("--reads %d must not be negative", cfg.reads)
	}
	if cfg.scenario == "point" && cfg.seed == 0 {
		return config{}, usagef("--seed must be positive")
	}
	if cfg.scenario == "ttl" {
		if cfg.generations <= 0 {
			return config{}, usagef("--generations %d must be positive", cfg.generations)
		}
		if cfg.ttl <= 0 {
			return config{}, usagef("--ttl %s must be positive", cfg.ttl)
		}
		if cfg.sweepPeriod <= 0 {
			return config{}, usagef("--sweep-period %s must be positive", cfg.sweepPeriod)
		}
	}
	if cfg.jsonOut != "" {
		if _, err := os.Stat(cfg.jsonOut); err == nil {
			return config{}, usagef("--json-out %s exists: a report file is one run's artifact", cfg.jsonOut)
		}
	}
	return cfg, nil
}

// run drives the declared scenario and prints its phases.
func run(cfg config, stdout io.Writer) error {
	var res bench.Result
	var err error
	switch cfg.scenario {
	case "append":
		res, err = bench.RunAppendOnly(cfg.dir, bench.AppendOnlySpec{
			Sessions:         cfg.sessions,
			EventsPerSession: cfg.events,
			ValueSize:        cfg.valueSize,
		})
	case "point":
		res, err = bench.RunPointInTime(cfg.dir, bench.PointInTimeSpec{
			Sessions:         cfg.sessions,
			EventsPerSession: cfg.events,
			ReadsPerSession:  cfg.reads,
			ValueSize:        cfg.valueSize,
			Seed:             cfg.seed,
		})
	case "ttl":
		res, err = bench.RunTTLMix(cfg.dir, bench.TTLMixSpec{
			Generations:      cfg.generations,
			SessionsPerGen:   cfg.sessions,
			EventsPerSession: cfg.events,
			ValueSize:        cfg.valueSize,
			TTL:              cfg.ttl,
			SweepPeriod:      cfg.sweepPeriod,
		})
	}
	if err != nil {
		return err
	}
	for _, pr := range res.Phases {
		if _, err := fmt.Fprintf(stdout, "%s %s: %d ops %s %.1f ops/sec p50 %v p99 %v p999 %v\n",
			res.Driver, pr.Phase, pr.Ops, pr.Elapsed.Round(time.Microsecond), pr.Throughput,
			pr.Latency.P50.Round(time.Microsecond), pr.Latency.P99.Round(time.Microsecond),
			pr.Latency.P999.Round(time.Microsecond)); err != nil {
			return err
		}
	}
	if cfg.jsonOut != "" {
		doc, err := bench.RenderJSON([]bench.Result{res})
		if err != nil {
			return err
		}
		if err := os.WriteFile(cfg.jsonOut, doc, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfg.jsonOut, err)
		}
	}
	return nil
}

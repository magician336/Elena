// Command bench is the harness CLI: it drives one engine through a
// micro workload — a timed append phase then a timed read phase — and
// prints each phase's throughput and latency numbers. Every driver is
// reached by name through the same lifecycle, so the same command
// compares engines at one declared durability level.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"elena/bench"
)

// defaultOps and defaultValueSize are the micro workload's defaults:
// sized for a seconds-scale end-to-end run, not a benchmark.
const (
	defaultOps       = 200
	defaultValueSize = 1024
)

// usageText is the public help surface. Every line leads with a system
// signal.
const usageText = `usage: bench [arguments]

drives one engine through a micro workload: a timed append phase, then a timed read phase

arguments:
  --driver elena|bbolt   engine to drive (required)
  --ops N                operations per phase (default 200)
  --value-size B         bytes per appended value (default 1024)
  --dir PATH             fresh directory for this run (required, must not exist)

output: per phase — operation count, wall time, throughput in ops/sec, and latency min/max/mean
durability: every append is synchronized on acknowledgment; both drivers ride the same regime
`

// usageError marks argument problems: Run prints the message and exits
// with code 2 instead of 1, separating "wrong ask" from "run failure".
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef builds a usageError.
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
	_, _ = fmt.Fprintf(stderr, "bench: %v\n", err)
	return 1
}

// config is one parsed command line.
type config struct {
	driver    string
	ops       int
	valueSize int
	dir       string
}

// parse turns the command line into a validated config.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	fs.StringVar(&cfg.driver, "driver", "", "engine to drive: elena or bbolt")
	fs.IntVar(&cfg.ops, "ops", defaultOps, "operations per phase")
	fs.IntVar(&cfg.valueSize, "value-size", defaultValueSize, "bytes per appended value")
	fs.StringVar(&cfg.dir, "dir", "", "fresh directory for this run")
	if err := fs.Parse(args); err != nil {
		return config{}, usagef("arguments: %v", err)
	}
	if fs.NArg() > 0 {
		return config{}, usagef("unexpected arguments: %v", fs.Args())
	}
	if cfg.driver == "" {
		return config{}, usagef("--driver is required")
	}
	if cfg.ops < 1 {
		return config{}, usagef("--ops %d must be at least 1", cfg.ops)
	}
	if cfg.valueSize < 1 {
		return config{}, usagef("--value-size %d must be at least 1", cfg.valueSize)
	}
	if cfg.dir == "" {
		return config{}, usagef("--dir is required")
	}
	return cfg, nil
}

// run drives the workload and writes the report lines.
func run(cfg config, stdout io.Writer) error {
	driver, err := bench.For(cfg.driver)
	if err != nil {
		return usagef("%v", err)
	}
	workload, err := bench.Micro(cfg.ops, cfg.valueSize)
	if err != nil {
		return usagef("%v", err)
	}
	if err := os.Mkdir(cfg.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", cfg.dir, err)
	}
	res, err := bench.Run(driver, cfg.dir, workload)
	if err != nil {
		return err
	}

	if err := writef(stdout, "driver %s, workload %s (%d ops, %dB values), dir %s\n",
		res.Driver, res.Workload, cfg.ops, cfg.valueSize, cfg.dir); err != nil {
		return err
	}
	for _, pr := range res.Phases {
		if err := writef(stdout, "phase %s: %d ops in %v, %.1f ops/sec, latency min %v max %v mean %v\n",
			pr.Phase, pr.Ops, pr.Elapsed.Round(time.Microsecond), pr.Throughput,
			pr.Latency.Min, pr.Latency.Max, pr.Latency.Mean); err != nil {
			return err
		}
	}
	return nil
}

// writef formats one report line.
func writef(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	return err
}

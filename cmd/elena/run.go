// Command elena is the store's CLI: one binary for diagnosis (status),
// export (dump), space reclamation (merge), baseline management
// (checkpoint), and recovery (restore). Every command is a thin
// orchestration of the elena library's public API — the binary holds no
// storage logic of its own.
package main

import (
	"errors"
	"fmt"
	"io"
)

// usageText is the public help surface. Every line leads with a system
// signal; the sweep and merge lines pin the boundary: CLI runs disable
// the background sweep, which defers physical reclamation only —
// visibility is lazy expiry and never depends on the sweep — and merge
// is the explicit reclamation entry, neither replacing nor requiring
// the sweep.
const usageText = `usage: elena <command> [arguments]

commands:
  status      <dir>                        overview: namespaces, sessions, trajectories, dead ratio, write amplification
  dump        <dir> [namespace [session]]  trajectory export: sequence numbers and append timestamps
  merge       <dir>                        space reclamation: one merge, stats before and after
  checkpoint  <dir>                        baseline: create one checkpoint, print its ID
  checkpoint  list <dir>                   baseline: list stored checkpoints
  restore     <dir> [id]                   recovery: restore a baseline, print the point-in-time read
  help                                     usage text

store: <dir> is the store directory, created if absent
sweep: off in every command; visibility never depends on it, only the timing of physical reclamation does
merge: the explicit reclamation entry; it neither replaces nor requires the background sweep
stats: dead ratio reads the directory's bytes; write amplification counts this process's writes
baselines: a restore resets runtime state; events past the baseline stay on disk, the printed read stops at the recorded sequence
events: printed with sequence numbers, append timestamps, byte sizes, quoted payload previews
`

// usageError marks argument problems: Run prints the message and exits
// with code 2 instead of 1, separating "wrong ask" from "store failure".
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef builds a usageError.
func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// Run dispatches one command line — args without the program name —
// against the injected writers and returns the process exit code: 0 on
// success, 2 on a usage problem, 1 on a store failure.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_ = printUsage(stderr) // a broken stderr leaves nowhere to report
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return finish("status", stderr, func() error { return runStatus(rest, stdout) })
	case "dump":
		return finish("dump", stderr, func() error { return runDump(rest, stdout) })
	case "merge":
		return finish("merge", stderr, func() error { return runMerge(rest, stdout) })
	case "checkpoint":
		return finish("checkpoint", stderr, func() error { return runCheckpoint(rest, stdout) })
	case "restore":
		return finish("restore", stderr, func() error { return runRestore(rest, stdout) })
	case "help", "-h", "--help":
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "command %q not found\n\n", cmd)
		_ = printUsage(stderr)
		return 2
	}
}

// printUsage writes the usage surface to w.
func printUsage(w io.Writer) error {
	_, err := fmt.Fprint(w, usageText)
	return err
}

// finish turns one command's outcome into an exit code: usage problems
// print their message (code 2), store failures print the command and
// the error (code 1), success stays silent and returns 0. A stderr too
// broken to carry the message leaves nowhere to report; the code still
// tells the story.
func finish(cmd string, stderr io.Writer, fn func() error) int {
	err := fn()
	var usage *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
}

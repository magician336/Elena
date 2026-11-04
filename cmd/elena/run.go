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
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return finish("status", stderr, func() error { return runStatus(rest, stdout) })
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "command %q not found\n\n", cmd)
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

// finish turns one command's outcome into an exit code: usage problems
// print their message (code 2), store failures print the command and
// the error (code 1), success stays silent and returns 0.
func finish(cmd string, stderr io.Writer, fn func() error) int {
	err := fn()
	var usage *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage):
		fmt.Fprintln(stderr, err)
		return 2
	default:
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
}

package main

import (
	"fmt"
	"io"

	"elena"
)

// runMerge triggers one merge — the explicit reclamation entry — and
// reports the Stats comparison around it: the same observables before
// and after, plus the dead bytes the merge reclaimed. The rewrite is
// disk traffic of its own, so the write-amplification figure can rise
// while the dead bytes fall; both sides of that trade print here.
func runMerge(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usagef("command merge wants exactly 1 argument: <dir>")
	}
	return withStore(args[0], func(store *elena.Store) error {
		before := store.Stats()
		if err := store.Merge(); err != nil {
			return err
		}
		after := store.Stats()
		if err := writef(stdout, "merge stats before: %s\n", statsLine(before)); err != nil {
			return err
		}
		if err := writef(stdout, "merge stats after:  %s\n", statsLine(after)); err != nil {
			return err
		}
		return writef(stdout, "merge reclaimed %d dead bytes\n", int64(before.DeadBytes)-int64(after.DeadBytes))
	})
}

// statsLine renders one Stats view for the merge comparison: the
// byte pair that bounds reclamation, the dead ratio, the write
// amplification, and the file count.
func statsLine(stats elena.Stats) string {
	return fmt.Sprintf("data bytes %d, dead bytes %d (%.1f%%), write amplification %.2f, files %d",
		stats.DataBytes, stats.DeadBytes, stats.DeadRatio*100, stats.WriteAmplification, len(stats.Files))
}

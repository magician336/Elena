package main

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"elena"
)

// runRestore resets the store's runtime view to a checkpoint baseline —
// the named checkpoint's, or the most recent one — and prints the
// point-in-time read that follows: every trajectory the manifest
// records, read through EventsUpTo at exactly the sequence number the
// checkpoint recorded for it. The read stops at each baseline; events
// appended after the checkpoint stay on disk — the baseline is a
// baseline, not a time machine — and the closing line says so.
func runRestore(args []string, stdout io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("command restore wants 1 or 2 arguments: <dir> [id]")
	}
	dir := args[0]
	return withStore(dir, func(store *elena.Store) error {
		id, err := checkpointToRestore(store, args)
		if err != nil {
			return err
		}
		if err := store.Restore(id); err != nil {
			return err
		}
		manifest, err := store.Checkpoint(id)
		if err != nil {
			return err
		}
		if err := writef(stdout, "checkpoint %d (created %s) restored\n", id, manifest.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := writef(stdout, "point-in-time read at the baseline:\n"); err != nil {
			return err
		}
		if err := printBaselineRead(store, manifest, stdout); err != nil {
			return err
		}
		return writef(stdout, "restore read stops at each baseline; events past it stay on disk\n")
	})
}

// checkpointToRestore picks the checkpoint to restore: the given ID
// when the caller named one, the most recent one otherwise. A store
// with no checkpoints has nothing to restore.
func checkpointToRestore(store *elena.Store, args []string) (uint64, error) {
	if len(args) == 2 {
		id, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return 0, usagef("command restore: checkpoint id %q is not a number", args[1])
		}
		return id, nil
	}
	checkpoints, err := store.Checkpoints()
	if err != nil {
		return 0, err
	}
	if len(checkpoints) == 0 {
		return 0, fmt.Errorf("checkpoints: none stored in %s, nothing to restore", args[0])
	}
	return checkpoints[len(checkpoints)-1].ID, nil
}

// printBaselineRead prints the manifest's own point-in-time read: one
// trajectory line per recorded baseline, naming the sequence number the
// checkpoint recorded, and below it the EventsUpTo result at exactly
// that endpoint — the baseline's timeline, whatever sits past it on
// disk. A session whose deadline has since passed reads as if never
// written, so its baseline prints no events at all and the session is
// skipped.
func printBaselineRead(store *elena.Store, manifest elena.CheckpointManifest, stdout io.Writer) error {
	for _, baseline := range manifest.Trajectories {
		sess, live, err := sessionHandle(store, baseline.Namespace, elena.SessionInfo{
			ID:       baseline.Session,
			Deadline: baseline.Deadline,
		})
		if err != nil {
			return err
		}
		if !live {
			continue
		}
		events, err := sess.EventsUpTo(baseline.ReachedSeq)
		if err != nil {
			return err
		}
		if err := writef(stdout, "trajectory %s/%s to seq %d\n", baseline.Namespace, baseline.Session, baseline.ReachedSeq); err != nil {
			return err
		}
		for _, event := range events {
			if err := writef(stdout, "  %s\n", eventLine(event)); err != nil {
				return err
			}
		}
	}
	return nil
}

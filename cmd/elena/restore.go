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
// point-in-time read that follows: every live trajectory's events up to
// its reached sequence number, read through EventsUpTo. In the
// kill-recovery scenario, nothing was appended past the checkpoint, so
// each trajectory's stored tail is the sequence number the manifest
// recorded and the printed read is the baseline's own timeline. The
// baseline is not a time machine: events appended after the checkpoint
// stay on disk, and the per-trajectory line names the sequence number
// the read actually reached.
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
		createdAt, err := checkpointTime(store, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "checkpoint %d (created %s) restored\n", id, createdAt.Format(time.RFC3339Nano))
		fmt.Fprintf(stdout, "point-in-time read at the baseline:\n")
		return printBaselineRead(store, stdout)
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

// printBaselineRead prints every live trajectory's point-in-time read:
// the trajectory line names the sequence number the read reaches — the
// stored tail — and the event lines below it are the EventsUpTo result
// at exactly that endpoint. A session whose deadline passed reads as if
// never written and prints nothing.
func printBaselineRead(store *elena.Store, stdout io.Writer) error {
	namespaces, err := store.Namespaces()
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		sessions, err := store.Sessions(namespace)
		if err != nil {
			return err
		}
		for _, info := range sessions {
			sess, live, err := sessionHandle(store, namespace, info)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			stored, err := sess.Events()
			if err != nil {
				return err
			}
			var tail uint64
			if len(stored) > 0 {
				tail = stored[len(stored)-1].Seq
			}
			events, err := sess.EventsUpTo(tail)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "trajectory %s/%s to seq %d\n", namespace, info.ID, tail)
			for _, event := range events {
				fmt.Fprintf(stdout, "  %s\n", eventLine(event))
			}
		}
	}
	return nil
}

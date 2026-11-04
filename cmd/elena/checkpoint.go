package main

import (
	"io"
	"time"

	"elena"
)

// runCheckpoint manages baselines: with a bare directory it creates
// one checkpoint of the whole store and prints its ID, creation time,
// and the live sessions the manifest records; with "list" it prints
// every stored checkpoint in creation order.
func runCheckpoint(args []string, stdout io.Writer) error {
	if len(args) >= 1 && args[0] == "list" {
		if len(args) != 2 {
			return usagef("command checkpoint list wants exactly 1 argument: <dir>")
		}
		return listCheckpoints(args[1], stdout)
	}
	if len(args) != 1 {
		return usagef("command checkpoint wants exactly 1 argument: <dir>; to list, use: checkpoint list <dir>")
	}
	dir := args[0]
	return withStore(dir, func(store *elena.Store) error {
		id, err := store.CreateCheckpoint()
		if err != nil {
			return err
		}
		live, err := liveSessionCount(store)
		if err != nil {
			return err
		}
		createdAt, err := checkpointTime(store, id)
		if err != nil {
			return err
		}
		return writef(stdout, "checkpoint %d created at %s, baseline of %d live sessions\n",
			id, createdAt.Format(time.RFC3339Nano), live)
	})
}

// listCheckpoints prints every stored checkpoint, oldest first — the
// order the listing API returns, which is creation order.
func listCheckpoints(dir string, stdout io.Writer) error {
	return withStore(dir, func(store *elena.Store) error {
		checkpoints, err := store.Checkpoints()
		if err != nil {
			return err
		}
		if err := writef(stdout, "checkpoints stored: %d\n", len(checkpoints)); err != nil {
			return err
		}
		for _, info := range checkpoints {
			if err := writef(stdout, "  checkpoint %d created %s\n", info.ID, info.CreatedAt.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return nil
	})
}

// liveSessionCount counts the live sessions across all namespaces —
// the sessions a freshly created manifest records.
func liveSessionCount(store *elena.Store) (int, error) {
	namespaces, err := store.Namespaces()
	if err != nil {
		return 0, err
	}
	live := 0
	for _, namespace := range namespaces {
		sessions, err := store.Sessions(namespace)
		if err != nil {
			return 0, err
		}
		live += len(sessions)
	}
	return live, nil
}

// checkpointTime looks one checkpoint's creation time up in its
// manifest — a single-key read, the ID CreateCheckpoint returns being
// the manifest's key.
func checkpointTime(store *elena.Store, id uint64) (time.Time, error) {
	manifest, err := store.Checkpoint(id)
	if err != nil {
		return time.Time{}, err
	}
	return manifest.CreatedAt, nil
}

package main

import (
	"io"
	"time"

	"elena"
)

// runStatus prints the store overview: every namespace with its live
// sessions and each trajectory's event count, then the merge view —
// dead ratio and write amplification straight from Stats, the engine's
// own observables forwarded unchanged. The CLI defines no metric of
// its own; the counts are the listed sessions and the read events.
func runStatus(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usagef("command status wants exactly 1 argument: <dir>")
	}
	dir := args[0]
	return withStore(dir, func(store *elena.Store) error {
		namespaces, err := store.Namespaces()
		if err != nil {
			return err
		}
		sessionsByNamespace := make([][]elena.SessionInfo, len(namespaces))
		totalSessions := 0
		for i, namespace := range namespaces {
			sessions, err := store.Sessions(namespace)
			if err != nil {
				return err
			}
			sessionsByNamespace[i] = sessions
			totalSessions += len(sessions)
		}

		stats := store.Stats()
		if err := writef(stdout, "store %s: %d namespaces, %d sessions, %d trajectories\n",
			dir, len(namespaces), totalSessions, totalSessions); err != nil {
			return err
		}
		if err := writef(stdout, "merge view: dead ratio %.1f%%, write amplification %.2f, data bytes %d, files %d\n",
			stats.DeadRatio*100, stats.WriteAmplification, stats.DataBytes, len(stats.Files)); err != nil {
			return err
		}

		for i, namespace := range namespaces {
			if err := writef(stdout, "namespace %s\n", namespace); err != nil {
				return err
			}
			for _, info := range sessionsByNamespace[i] {
				count, err := eventCount(store, namespace, info)
				if err != nil {
					return err
				}
				lifecycle := "perpetual"
				if !info.Deadline.IsZero() {
					lifecycle = "expires " + info.Deadline.Format(time.RFC3339)
				}
				if err := writef(stdout, "  session %s: %s, events %d\n", info.ID, lifecycle, count); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// eventCount reads one trajectory's event count the listing way: a
// full read of the trajectory, the events themselves the only
// observable behind the number.
func eventCount(store *elena.Store, namespace string, info elena.SessionInfo) (int, error) {
	sess, live, err := sessionHandle(store, namespace, info)
	if err != nil || !live {
		return 0, err
	}
	events, err := sess.Events()
	if err != nil {
		return 0, err
	}
	return len(events), nil
}

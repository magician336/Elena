package main

import (
	"fmt"
	"io"

	"elena"
)

// runDump exports the store as human-readable text: the whole store,
// one namespace, or one trajectory — every event with its sequence
// number, append timestamp, byte size, and a quoted payload preview.
func runDump(args []string, stdout io.Writer) error {
	if len(args) < 1 || len(args) > 3 {
		return usagef("command dump wants 1 to 3 arguments: <dir> [namespace [session]]")
	}
	dir := args[0]
	return withStore(dir, func(store *elena.Store) error {
		namespaces, err := store.Namespaces()
		if err != nil {
			return err
		}
		if len(args) >= 2 {
			if !listedNamespace(namespaces, args[1]) {
				return fmt.Errorf("namespace %q not stored in %s", args[1], dir)
			}
			namespaces = []string{args[1]}
		}

		for _, namespace := range namespaces {
			sessions, err := store.Sessions(namespace)
			if err != nil {
				return err
			}
			if len(args) == 3 && !listedSession(sessions, args[2]) {
				return fmt.Errorf("session %q not stored in namespace %q", args[2], namespace)
			}
			fmt.Fprintf(stdout, "namespace %s\n", namespace)
			for _, info := range sessions {
				if len(args) == 3 && info.ID != args[2] {
					continue
				}
				if err := dumpTrajectory(store, namespace, info, stdout); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// dumpTrajectory prints one session's trajectory: the session line and
// one event line per stored event. A session whose deadline passed
// between listing and reading prints no events — it reads as if never
// written, so there is nothing to export.
func dumpTrajectory(store *elena.Store, namespace string, info elena.SessionInfo, stdout io.Writer) error {
	fmt.Fprintf(stdout, "  session %s\n", info.ID)
	sess, live, err := sessionHandle(store, namespace, info)
	if err != nil || !live {
		return err
	}
	events, err := sess.Events()
	if err != nil {
		return err
	}
	for _, event := range events {
		fmt.Fprintf(stdout, "    %s\n", eventLine(event))
	}
	return nil
}

// listedNamespace reports whether name appears in the listing.
func listedNamespace(namespaces []string, name string) bool {
	for _, namespace := range namespaces {
		if namespace == name {
			return true
		}
	}
	return false
}

// listedSession reports whether id appears in the listing.
func listedSession(sessions []elena.SessionInfo, id string) bool {
	for _, info := range sessions {
		if info.ID == id {
			return true
		}
	}
	return false
}

package main

import (
	"errors"
	"time"

	"elena"
)

// openCLI opens the store for one command run. The sweep is off in
// every CLI process: a short-lived command starts no background work,
// and visibility never depends on the sweep — lazy expiry holds it —
// only the timing of physical reclamation does, and merge is its
// explicit entry here.
func openCLI(dir string) (*elena.Store, error) {
	return elena.Open(dir, elena.WithSweepDisabled())
}

// withStore opens the store rooted at dir for one command run, hands it
// to fn, and closes it afterwards. A failure inside fn wins; a close
// failure after a successful run surfaces too, so no error is swallowed.
func withStore(dir string, fn func(*elena.Store) error) (err error) {
	store, err := openCLI(dir)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := store.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return fn(store)
}

// sessionHandle returns a read handle for one session the listing
// reported live, without writing: creating an existing perpetual
// session only hands out the handle, and creating an existing timed
// session with its deadline's remaining TTL is idempotent — the stored
// deadline governs, the passed TTL is consulted only at first creation.
// A session whose deadline passes between the listing and this call
// reports elapsed and reads as if never written, so the caller treats
// it as absent; live reports whether the handle exists.
func sessionHandle(store *elena.Store, namespace string, info elena.SessionInfo) (sess *elena.Session, live bool, err error) {
	if info.Deadline.IsZero() {
		sess, err := store.CreateSession(namespace, info.ID)
		return sess, err == nil, err
	}
	ttl := time.Until(info.Deadline)
	if ttl <= 0 {
		return nil, false, nil
	}
	sess, err = store.CreateSessionWithTTL(namespace, info.ID, ttl)
	if err != nil {
		if errors.Is(err, elena.ErrInvalidTTL) {
			return nil, false, nil // the deadline passed between listing and call
		}
		return nil, false, err
	}
	return sess, true, nil
}

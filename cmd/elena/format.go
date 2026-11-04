package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"elena"
)

// previewWidth bounds an event's payload preview: quoted bytes up to
// this many, a marker for the rest — the stored payload stays opaque
// and whole, the dump shows enough to inspect.
const previewWidth = 64

// writef formats one piece of output onto w, surfacing write failures —
// a closed pipe down the line — as the command's error.
func writef(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	return err
}

// eventLine renders one event for the human-readable exports: sequence
// number, append timestamp, payload size, and the payload preview.
func eventLine(event elena.Event) string {
	return fmt.Sprintf("event #%d  %s  %d bytes  %s",
		event.Seq, event.AppendedAt.Format(time.RFC3339Nano), len(event.Payload), quotePreview(event.Payload))
}

// quotePreview renders a payload Go-quoted — non-printable and
// non-UTF-8 bytes appear as escapes, so any payload renders as text.
func quotePreview(payload []byte) string {
	if len(payload) > previewWidth {
		return fmt.Sprintf("%q ...", payload[:previewWidth])
	}
	return fmt.Sprintf("%q", payload)
}

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

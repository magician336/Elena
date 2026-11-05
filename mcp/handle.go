package mcp

import (
	"errors"
	"fmt"
	"time"

	"github.com/magician336/elena"
)

// handleTTL is the TTL writeHandle offers when re-deriving the handle
// of an existing timed session. The library consults the argument only
// when the session is first created; for the existing session the
// stored deadline governs, so the value never rewrites one. It applies
// only in the freed-name race, where the fresh session is timed anyway.
const handleTTL = time.Hour

// writeHandle derives a session handle for the write path. The
// library's create calls are its only handle entry points, so an
// absent name starts a perpetual session — the documented behavior of
// append_event — and a stored timed session yields its handle through
// the idempotent timed form.
func (s *Server) writeHandle(namespace, session string) (*elena.Session, error) {
	sess, err := s.store.CreateSession(namespace, session)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, elena.ErrSessionExists) {
		return nil, fmt.Errorf("opening session %s/%s: %w", namespace, session, err)
	}
	sess, err = s.store.CreateSessionWithTTL(namespace, session, handleTTL)
	if err != nil {
		return nil, fmt.Errorf("opening timed session %s/%s: %w", namespace, session, err)
	}
	return sess, nil
}

// errSessionAbsent marks a name the read path did not find. The read
// tool turns it into an empty trajectory instead of a tool error, so a
// name nobody wrote reads like the library itself would read it: as if
// never written.
var errSessionAbsent = fmt.Errorf("session absent from the namespace listing")

// readHandle derives a session handle for the read path without
// creating anything: the namespace listing tells whether the name is
// live and which lifecycle form it holds, and the matching create call
// — idempotent for an existing session of the same form — hands out
// the handle. A timed session whose deadline elapsed is absent from
// the listing and reads as if never written, matching the library's
// own read path.
func (s *Server) readHandle(namespace, session string) (*elena.Session, error) {
	timed, err := s.listedForm(namespace, session)
	if err != nil {
		return nil, err
	}
	if timed {
		sess, err := s.store.CreateSessionWithTTL(namespace, session, handleTTL)
		if err != nil {
			return nil, fmt.Errorf("opening timed session %s/%s: %w", namespace, session, err)
		}
		return sess, nil
	}
	sess, err := s.store.CreateSession(namespace, session)
	if err != nil {
		return nil, fmt.Errorf("opening session %s/%s: %w", namespace, session, err)
	}
	return sess, nil
}

// listedForm reports whether the session is live and whether it is
// timed. An absent or elapsed name returns errSessionAbsent wrapped
// around the identity, so callers can distinguish it from store
// failures.
func (s *Server) listedForm(namespace, session string) (timed bool, err error) {
	infos, err := s.store.Sessions(namespace)
	if err != nil {
		return false, fmt.Errorf("listing sessions of namespace %s: %w", namespace, err)
	}
	for _, info := range infos {
		if info.ID != session {
			continue
		}
		return !info.Deadline.IsZero(), nil
	}
	return false, fmt.Errorf("%s/%s: %w", namespace, session, errSessionAbsent)
}

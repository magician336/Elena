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

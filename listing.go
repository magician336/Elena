package elena

import "time"

// SessionInfo describes one live session of a namespace: its ID within
// the namespace and the deadline at which the whole session reads as if
// never written.
type SessionInfo struct {
	// ID is the session's ID within its namespace.
	ID string

	// Deadline is when the session's TTL elapses; the zero time marks a
	// perpetual session, which has none.
	Deadline time.Time
}

// Namespaces returns every namespace that holds at least one live
// session, sorted. A namespace whose sessions have all elapsed their
// TTL holds nothing left to find and is not listed: the namespaces of a
// store are discovered through what remains readable in them.
func (s *Store) Namespaces() ([]string, error) {
	return s.store.Namespaces()
}

// Sessions returns the live sessions of namespace, sorted by session
// ID. A session is live until its TTL elapses, so the listing follows
// the stored entries: a session whose deadline has passed is absent
// from it, whichever process looks and whenever the store was last
// opened. The namespace must be non-empty and within the documented
// label limit (an error wrapping ErrInvalidLabel otherwise).
func (s *Store) Sessions(namespace string) ([]SessionInfo, error) {
	inner, err := s.store.Sessions(namespace)
	if err != nil {
		return nil, err
	}
	sessions := make([]SessionInfo, len(inner))
	for i, info := range inner {
		sessions[i] = SessionInfo{ID: info.ID, Deadline: info.Deadline}
	}
	return sessions, nil
}

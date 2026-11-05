package semantic

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/magician336/elena/internal/engine"
)

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
// TTL holds nothing left to find and is not listed; the bytes of its
// sessions stay on disk until a merge reclaims them, invisible all the
// while.
func (s *Store) Namespaces() ([]string, error) {
	seen := make(map[string]struct{})
	err := s.eng.Scan(engine.ScanOptions{Prefix: []byte{keyAreaSession}}, func(k, _ []byte) error {
		namespace, _, kind, ok := parseSessionKey(k)
		if !ok {
			return fmt.Errorf("elena: list namespaces: %w", errCorruptedSessionKey)
		}
		if kind == keyKindMetadata {
			seen[namespace] = struct{}{}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedSessionKey) {
			return nil, err
		}
		return nil, opErr("list namespaces", err)
	}

	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

// Sessions returns the live sessions of namespace, sorted by session
// ID. A session is live until its TTL elapses; perpetual sessions are
// always live. The listing follows the stored entries, so a session
// whose deadline has passed is absent no matter which process looks.
func (s *Store) Sessions(namespace string) ([]SessionInfo, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("elena: list sessions of %q: %w", namespace, err)
	}

	var sessions []SessionInfo
	err := s.eng.Scan(engine.ScanOptions{Prefix: namespacePrefix(namespace)}, func(k, value []byte) error {
		_, id, kind, ok := parseSessionKey(k)
		if !ok {
			return fmt.Errorf("elena: list sessions of %q: %w", namespace, errCorruptedSessionKey)
		}
		if kind != keyKindMetadata {
			return nil
		}
		_, deadline, derr := decodeMetadata(value)
		if derr != nil {
			return fmt.Errorf("elena: list sessions of %q: %w", namespace, derr)
		}
		sessions = append(sessions, SessionInfo{ID: id, Deadline: deadline})
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedSessionKey) || errors.Is(err, errMalformedMetadata) {
			return nil, err
		}
		return nil, opErr("list sessions", err)
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

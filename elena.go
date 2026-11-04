// Package elena provides an embedded state store: sessions grouped by
// namespace own append-only trajectories of numbered, timestamped
// events, durably stored in a single directory.
package elena

import (
	"fmt"
	"time"

	"elena/internal/semantic"
)

// Store is a trajectory store rooted at one directory. All methods are
// safe for use from multiple goroutines.
type Store struct {
	store *semantic.Store
}

// Open opens the store rooted at dir, creating the directory if
// needed, configured by opts. The store holds an exclusive lock on its
// directory: while it is open, a second Open of the same directory
// fails with an error wrapping ErrDirectoryLocked.
//
// By default a background sweep runs: once per DefaultSweepPeriod it
// checks the timed sessions' deadlines and, once one has elapsed,
// triggers the merge that physically reclaims the session's bytes —
// visibility of an elapsed session is immediate on the read path
// either way; the sweep governs only how promptly the bytes come back.
// WithSweepPeriod tunes the period and WithSweepDisabled turns the
// sweep off entirely.
func Open(dir string, opts ...OpenOption) (*Store, error) {
	var o openOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	cfg, err := o.config()
	if err != nil {
		return nil, fmt.Errorf("elena: open %q: %w", dir, err)
	}
	store, err := semantic.Open(dir, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{store: store}, nil
}

// Close closes the store and releases its directory lock. Events
// acknowledged before Close returns are durable. It is safe to call
// Close more than once; repeated calls are no-ops.
func (s *Store) Close() error {
	return s.store.Close()
}

// Merge triggers one merge: the store's live entries are rewritten into
// fresh data files while every expired entry is left out — that omission
// is the physical reclamation. One Merge reclaims the bytes and index
// memory of every elapsed session on disk — those the background sweep
// tracks and any expired residue it never saw, for instance from before
// the current Open. Visibility never moves: elapsed sessions read as if
// never written before the merge and still do after it. Operations after
// Close fail with an error wrapping ErrClosed.
func (s *Store) Merge() error {
	return s.store.Merge()
}

// Session is a handle to one session's trajectory. Handles obtained
// from repeated CreateSession calls share the trajectory's state. All
// methods are safe for use from multiple goroutines.
type Session struct {
	session *semantic.Session
}

// CreateSession creates the perpetual session named session in
// namespace and returns a handle for appending to and reading its
// trajectory. Creating an existing perpetual session returns a handle
// that continues the stored trajectory: its sequence numbers continue
// from the last stored event, across Close and Open. Creating a session
// that exists with a TTL fails with an error wrapping ErrSessionExists
// — unless that TTL has already elapsed, in which case the session
// reads as if never written and the name is free to start fresh.
func (s *Store) CreateSession(namespace, session string) (*Session, error) {
	inner, err := s.store.CreateSession(namespace, session)
	if err != nil {
		return nil, err
	}
	return &Session{session: inner}, nil
}

// CreateSessionWithTTL creates the session named session in namespace
// with a time-to-live and returns a handle for appending to and reading
// its trajectory. The session's metadata entry and every event of its
// trajectory share one deadline: once the TTL has elapsed, the whole
// session — events included — reads as if never written, in listings,
// full reads, and single-event reads alike. The verdict travels with
// the stored entries, so it survives Close and Open. The ttl must be
// positive and its deadline storable (an error wrapping ErrInvalidTTL
// otherwise).
//
// Creating a session that already exists: with a TTL it is idempotent —
// the call returns a handle to the stored session and the ttl argument
// is consulted only when the session is first created, so the recorded
// deadline governs and a TTL cannot be renewed (out of scope). Once
// that deadline has elapsed the session is gone as if never written and
// the name is free: creating it again starts a fresh session, with a
// fresh trajectory, in either lifecycle form. Creating a perpetual
// session that exists with a TTL (or the reverse) fails with an error
// wrapping ErrSessionExists.
func (s *Store) CreateSessionWithTTL(namespace, session string, ttl time.Duration) (*Session, error) {
	inner, err := s.store.CreateSessionWithTTL(namespace, session, ttl)
	if err != nil {
		return nil, err
	}
	return &Session{session: inner}, nil
}

// Append appends payload to the trajectory as its next event and
// returns the event's sequence number, starting at 1. The payload is
// copied and treated as opaque bytes; an acknowledged append is durable
// the moment Append returns. Appends from concurrent goroutines hand
// out a hole-free, strictly increasing sequence. Appending to a timed
// session whose TTL has elapsed fails with an error wrapping
// ErrSessionEnded — the session already reads as if never written.
func (sess *Session) Append(payload []byte) (uint64, error) {
	return sess.session.Append(payload)
}

// Event is one immutable append to a trajectory, read back
// self-describing: the sequence number and the append time travel with
// the payload.
type Event struct {
	// Seq is the event's sequence number, assigned at append time,
	// starting at 1 and increasing by one per event.
	Seq uint64

	// AppendedAt is when the event was appended.
	AppendedAt time.Time

	// Payload is the caller-supplied bytes, stored and returned
	// unmodified.
	Payload []byte
}

// Events returns every event of the trajectory in sequence order. The
// read is a snapshot: events appended after it starts are not included.
func (sess *Session) Events() ([]Event, error) {
	inner, err := sess.session.Events()
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(inner))
	for i, ev := range inner {
		events[i] = Event{Seq: ev.Seq, AppendedAt: ev.AppendedAt, Payload: ev.Payload}
	}
	return events, nil
}

// Event returns the single event with the given sequence number, or an
// error wrapping ErrEventNotFound when the trajectory holds no such
// event.
func (sess *Session) Event(seq uint64) (Event, error) {
	inner, err := sess.session.Event(seq)
	if err != nil {
		return Event{}, err
	}
	return Event{Seq: inner.Seq, AppendedAt: inner.AppendedAt, Payload: inner.Payload}, nil
}

// ErrDirectoryLocked is wrapped into the error Open returns when the
// directory is already held by another open store.
var ErrDirectoryLocked = semantic.ErrDirectoryLocked

// ErrEventNotFound is wrapped into the error Event returns when the
// trajectory holds no event with the requested sequence number.
var ErrEventNotFound = semantic.ErrEventNotFound

// ErrInvalidLabel is wrapped into the error CreateSession returns when
// the namespace or the session ID is empty or too long.
var ErrInvalidLabel = semantic.ErrInvalidLabel

// ErrInvalidTTL is wrapped into the error CreateSessionWithTTL returns
// when the TTL cannot define a future deadline: zero, negative, or
// beyond the storable range.
var ErrInvalidTTL = semantic.ErrInvalidTTL

// ErrInvalidSweepPeriod is wrapped into the error Open returns when
// WithSweepPeriod is given a period that cannot define a check
// interval: zero or negative. Turning the sweep off is its own explicit
// option, WithSweepDisabled, not a period value.
var ErrInvalidSweepPeriod = semantic.ErrInvalidSweepPeriod

// ErrSessionExists is wrapped into the error the create calls return
// when the named session already exists with a different lifecycle
// form — perpetual against timed, in either direction. The lifecycle is
// a property of the stored session, and a TTL cannot be renewed once
// set, so re-creating across forms fails instead of silently returning
// a session with a different lifetime than requested.
var ErrSessionExists = semantic.ErrSessionExists

// ErrSessionEnded is wrapped into the error Append returns when the
// session's TTL has elapsed: the session — metadata and events —
// already reads as if never written, and a write now would resurrect
// half of one. Create the name again for a fresh session.
var ErrSessionEnded = semantic.ErrSessionEnded

// ErrClosed is wrapped into the errors of operations attempted after
// Close.
var ErrClosed = semantic.ErrClosed

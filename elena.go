// Package elena provides an embedded state store: sessions grouped by
// namespace own append-only trajectories of numbered, timestamped
// events, durably stored in a single directory.
package elena

import (
	"time"

	"elena/internal/semantic"
)

// Store is a trajectory store rooted at one directory. All methods are
// safe for use from multiple goroutines.
type Store struct {
	store *semantic.Store
}

// Open opens the store rooted at dir, creating the directory if
// needed. The store holds an exclusive lock on its directory: while it
// is open, a second Open of the same directory fails with an error
// wrapping ErrDirectoryLocked.
func Open(dir string) (*Store, error) {
	store, err := semantic.Open(dir)
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

// Session is a handle to one session's trajectory. Handles obtained
// from repeated CreateSession calls share the trajectory's state. All
// methods are safe for use from multiple goroutines.
type Session struct {
	session *semantic.Session
}

// CreateSession creates the perpetual session named session in
// namespace and returns a handle for appending to and reading its
// trajectory. Creating an existing session returns a handle that
// continues the stored trajectory: its sequence numbers continue from
// the last stored event, across Close and Open.
func (s *Store) CreateSession(namespace, session string) (*Session, error) {
	inner, err := s.store.CreateSession(namespace, session)
	if err != nil {
		return nil, err
	}
	return &Session{session: inner}, nil
}

// Append appends payload to the trajectory as its next event and
// returns the event's sequence number, starting at 1. The payload is
// copied and treated as opaque bytes; an acknowledged append is durable
// the moment Append returns. Appends from concurrent goroutines hand
// out a hole-free, strictly increasing sequence.
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

// ErrClosed is wrapped into the errors of operations attempted after
// Close.
var ErrClosed = semantic.ErrClosed

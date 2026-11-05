package semantic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/magician336/elena/internal/engine"
)

// The session metadata entry: a version byte plus a lifecycle form
// byte. The perpetual form is those two bytes alone; the timed form
// appends the session deadline — Unix nanoseconds, big-endian — after
// the form byte, extending the layout without breaking it. A timed
// session's metadata entry and every event of its trajectory carry one
// and the same deadline, so the session turns invisible as a whole.
const (
	metadataVersion       byte = 0x01
	metadataFormPerpetual byte = 0x00
	metadataFormTimed     byte = 0x01

	// metadataTimedSize is the byte size of a timed metadata entry:
	// version, form, deadline.
	metadataTimedSize = 2 + 8
)

// ErrInvalidLabel is returned by CreateSession when the namespace or
// the session ID is empty or exceeds the documented label limit.
var ErrInvalidLabel = errors.New("elena: namespace and session ID must be non-empty and at most 480 bytes each")

// ErrInvalidTTL is returned by CreateSessionWithTTL when the TTL cannot
// define a future deadline: zero, negative, or beyond the storable
// range.
var ErrInvalidTTL = errors.New("elena: session ttl must be positive and storable")

// ErrSessionExists is returned by the create calls when the named
// session already exists with a different lifecycle form — perpetual
// against timed, in either direction. The lifecycle is a property of
// the stored session, not of the handle a call asks for; a timed
// session's deadline cannot be changed once set (renewal is out of
// scope), so re-creating across forms fails instead of silently
// returning a session with a different lifetime than requested.
var ErrSessionExists = errors.New("elena: session already exists with a different lifecycle form")

// ErrSessionEnded is returned by Append when the session's TTL has
// elapsed: the session — metadata and events — already reads as if
// never written, and a write now would resurrect half of one. Create
// the name again for a fresh session.
var ErrSessionEnded = errors.New("elena: session TTL elapsed")

var (
	errMalformedMetadata = errors.New("elena: malformed session metadata")
	errDetachedSession   = errors.New("elena: session handle detached from its store")
	errSequenceExhausted = errors.New("elena: trajectory sequence numbers exhausted")
	errPayloadTooLarge   = errors.New("elena: payload exceeds the storable size")
)

// encodePerpetualMetadata returns the metadata entry of a perpetual
// session: version and form byte, nothing more.
func encodePerpetualMetadata() []byte {
	return []byte{metadataVersion, metadataFormPerpetual}
}

// encodeTimedMetadata returns the metadata entry of a session whose
// deadline is deadline.
func encodeTimedMetadata(deadline time.Time) []byte {
	value := make([]byte, metadataTimedSize)
	value[0] = metadataVersion
	value[1] = metadataFormTimed
	binary.BigEndian.PutUint64(value[2:], uint64(deadline.UnixNano()))
	return value
}

// decodeMetadata validates a stored metadata entry and returns the
// lifecycle form it records: perpetual, or timed with its deadline. A
// timed entry the engine still reports is one whose deadline has not
// elapsed, so the decoded deadline is always in the future.
func decodeMetadata(value []byte) (lifecycleForm, time.Time, error) {
	switch {
	case len(value) == 2 && value[0] == metadataVersion && value[1] == metadataFormPerpetual:
		return lifecycleForm{}, time.Time{}, nil
	case len(value) == metadataTimedSize && value[0] == metadataVersion && value[1] == metadataFormTimed:
		deadline := time.Unix(0, int64(binary.BigEndian.Uint64(value[2:])))
		if deadline.IsZero() {
			// The zero deadline belongs to the perpetual form alone.
			return lifecycleForm{}, time.Time{}, errMalformedMetadata
		}
		return lifecycleForm{timed: true}, deadline, nil
	default:
		return lifecycleForm{}, time.Time{}, errMalformedMetadata
	}
}

// trajKey identifies one trajectory: the namespace and session ID the
// caller named at creation time.
type trajKey struct {
	namespace string
	session   string
}

// display renders the key for error messages.
func (k trajKey) display() string {
	return k.namespace + "/" + k.session
}

// trajectory is the runtime state of one trajectory: the last sequence
// number handed out and, for a timed session, the deadline every entry
// of the session shares — the zero time marks a perpetual session. It
// is guarded by Store.mu and rebuilt from the stored trajectory
// whenever a session is attached to a fresh Store.
type trajectory struct {
	lastSeq  uint64
	deadline time.Time
}

// lifecycleForm is the lifecycle a create call asks the session to have.
type lifecycleForm struct {
	// timed distinguishes the TTL form from the perpetual form.
	timed bool

	// ttl is the requested time-to-live, consulted only when the
	// session is first created. Valid when timed is set.
	ttl time.Duration
}

// timed reports whether the trajectory belongs to a session with a
// deadline.
func (t *trajectory) timed() bool {
	return !t.deadline.IsZero()
}

// elapsed reports whether a timed trajectory's deadline has passed. A
// perpetual trajectory never elapses.
func (t *trajectory) elapsed(now time.Time) bool {
	return t.timed() && !now.Before(t.deadline)
}

// Session is a handle to one session's trajectory. Handles obtained
// from repeated CreateSession calls share the trajectory's state.
// All methods are safe for use from multiple goroutines.
type Session struct {
	store *Store
	key   trajKey
}

// CreateSession creates the perpetual session named session in
// namespace and returns a handle for appending to and reading its
// trajectory. Creating an existing session returns a handle that
// continues the stored trajectory: its sequence numbers continue from
// the last stored event, across Close and Open.
func (s *Store) CreateSession(namespace, session string) (*Session, error) {
	return s.createSession(namespace, session, lifecycleForm{})
}

// CreateSessionWithTTL creates the session named session in namespace
// with a time-to-live and returns a handle for appending to and reading
// its trajectory. The session's metadata entry and every event of its
// trajectory share one deadline: once the TTL has elapsed, the whole
// session — events included — reads as if never written, and the verdict
// travels with the stored entries, so it survives Close and Open.
func (s *Store) CreateSessionWithTTL(namespace, session string, ttl time.Duration) (*Session, error) {
	return s.createSession(namespace, session, lifecycleForm{timed: true, ttl: ttl})
}

// createSession validates the labels, brings the named session into
// this store's runtime under the requested lifecycle form, and returns a
// handle. The caller-visible semantics live on the public wrappers.
func (s *Store) createSession(namespace, session string, form lifecycleForm) (*Session, error) {
	key := trajKey{namespace: namespace, session: session}
	if err := validateLabels(key); err != nil {
		return nil, fmt.Errorf("elena: create session %q: %w", key.display(), err)
	}
	if form.timed && form.ttl <= 0 {
		return nil, fmt.Errorf("elena: create session %q: ttl %d: %w", key.display(), form.ttl, ErrInvalidTTL)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if traj, ok := s.trajectories[key]; ok {
		switch {
		case traj.elapsed(time.Now()):
			// The session's TTL elapsed: it reads as if never written,
			// so the name is free — fall through and create it anew.
		case traj.timed() == form.timed:
			return &Session{store: s, key: key}, nil
		default:
			return nil, fmt.Errorf("elena: create session %q: %w", key.display(), ErrSessionExists)
		}
	}

	traj, err := s.attachTrajectory(key, form)
	if err != nil {
		return nil, err
	}
	s.trajectories[key] = traj
	if traj.timed() {
		// A timed session enters the sweep's deadline view: this store
		// now owes a merge once the deadline elapses. A re-created name
		// overwrites whatever deadline the view held for it; a perpetual
		// session never enters, and attaching one leaves any elapsed
		// entry in place — the signal that expired bytes are due a
		// reclaim.
		s.deadlines[key] = traj.deadline
	}
	return &Session{store: s, key: key}, nil
}

// validateLabels rejects empty or oversized namespace and session IDs.
func validateLabels(key trajKey) error {
	if err := validateNamespace(key.namespace); err != nil {
		return err
	}
	if len(key.session) == 0 || len(key.session) > maxLabelBytes {
		return ErrInvalidLabel
	}
	return nil
}

// validateNamespace rejects an empty or oversized namespace.
func validateNamespace(namespace string) error {
	if len(namespace) == 0 || len(namespace) > maxLabelBytes {
		return ErrInvalidLabel
	}
	return nil
}

// attachTrajectory brings a trajectory into this store's runtime: it
// ensures the session's metadata entry exists on disk under the
// requested lifecycle form and rebuilds the sequence counter from the
// stored trajectory. A session found on disk attaches as stored — a
// timed session under its recorded deadline, whatever TTL the attaching
// call named. The caller holds s.mu.
func (s *Store) attachTrajectory(key trajKey, form lifecycleForm) (*trajectory, error) {
	meta := metadataKey(key.namespace, key.session)
	var deadline time.Time
	value, err := s.eng.Get(meta)
	switch {
	case err == nil:
		storedForm, storedDeadline, derr := decodeMetadata(value)
		if derr != nil {
			return nil, fmt.Errorf("elena: attach session %q: %w", key.display(), derr)
		}
		if storedForm.timed != form.timed {
			return nil, fmt.Errorf("elena: create session %q: %w", key.display(), ErrSessionExists)
		}
		deadline = storedDeadline
	case errors.Is(err, engine.ErrNotFound):
		metaValue := encodePerpetualMetadata()
		if form.timed {
			deadline = time.Now().Add(form.ttl)
			metaValue = encodeTimedMetadata(deadline)
		}
		if err := s.putEntry("create session", meta, metaValue, deadline, ErrInvalidTTL); err != nil {
			return nil, err
		}
	default:
		return nil, opErr("create session", err)
	}

	last, err := s.lastSequence(key)
	if err != nil {
		return nil, err
	}
	return &trajectory{lastSeq: last, deadline: deadline}, nil
}

// putEntry writes one entry of a session: perpetually live when the
// deadline is the zero time, or carrying the session's deadline through
// the engine's TTL primitive, so a timed session and its events turn
// invisible together. A TTL the engine rejects maps to ttlErr, so
// engine vocabulary never leaks through the semantic layer. The caller
// holds s.mu.
func (s *Store) putEntry(op string, key, value []byte, deadline time.Time, ttlErr error) error {
	if deadline.IsZero() {
		if err := s.eng.Put(key, value); err != nil {
			return opErr(op, err)
		}
		return nil
	}
	if err := s.eng.PutWithTTL(key, value, time.Until(deadline)); err != nil {
		if errors.Is(err, engine.ErrInvalidTTL) {
			return fmt.Errorf("elena: %s: %w", op, ttlErr)
		}
		return opErr(op, err)
	}
	return nil
}

// lastSequence rebuilds the trajectory's sequence counter by scanning
// its stored event keys: the last key in byte order carries the highest
// sequence number, because byte order equals numeric order by
// construction. An empty trajectory reports 0.
func (s *Store) lastSequence(key trajKey) (uint64, error) {
	prefix := eventPrefix(key.namespace, key.session)
	var last uint64
	err := s.eng.Scan(engine.ScanOptions{Prefix: prefix}, func(k, _ []byte) error {
		seq, ok := sequenceFromEventKey(prefix, k)
		if !ok {
			return fmt.Errorf("elena: scan trajectory of %q: %w", key.display(), errCorruptedEvent)
		}
		last = seq
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedEvent) {
			return 0, err
		}
		return 0, opErr("scan trajectory", err)
	}
	return last, nil
}

// Append appends payload to the trajectory as its next event and
// returns the event's sequence number, starting at 1. The payload is
// copied and treated as opaque bytes; an acknowledged append is durable
// the moment Append returns under the default sync policy, and the
// other tiers trade that away (see Config.SyncPolicy). An event of a
// timed session carries the
// session's deadline, so the whole trajectory turns invisible together;
// appending once that deadline has passed fails with an error wrapping
// ErrSessionEnded, and no sequence number is handed out for it.
//
// Sequence allocation and the append form one atomic step under the
// store's write lock, so appends from concurrent goroutines hand out a
// hole-free, strictly increasing sequence and events reach the disk in
// sequence order. A failed append hands out no sequence number.
func (sess *Session) Append(payload []byte) (uint64, error) {
	st := sess.store
	st.mu.Lock()
	defer st.mu.Unlock()

	traj, ok := st.trajectories[sess.key]
	if !ok {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), errDetachedSession)
	}
	if traj.elapsed(time.Now()) {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), ErrSessionEnded)
	}
	if traj.lastSeq == math.MaxUint64 {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), errSequenceExhausted)
	}
	if uint64(len(payload)) > math.MaxUint32 {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), errPayloadTooLarge)
	}

	seq := traj.lastSeq + 1
	key := eventKey(sess.key.namespace, sess.key.session, seq)
	value := encodeEnvelope(seq, time.Now(), payload)
	if err := st.putEntry("append event", key, value, traj.deadline, ErrSessionEnded); err != nil {
		return 0, err
	}
	traj.lastSeq = seq
	return seq, nil
}

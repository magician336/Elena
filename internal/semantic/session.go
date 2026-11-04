package semantic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"elena/internal/engine"
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.trajectories[key]; !ok {
		traj, err := s.attachTrajectory(key, form)
		if err != nil {
			return nil, err
		}
		s.trajectories[key] = traj
	}
	return &Session{store: s, key: key}, nil
}

// validateLabels rejects empty or oversized namespace and session IDs.
func validateLabels(key trajKey) error {
	if len(key.namespace) == 0 || len(key.namespace) > maxLabelBytes ||
		len(key.session) == 0 || len(key.session) > maxLabelBytes {
		return ErrInvalidLabel
	}
	return nil
}

// attachTrajectory brings a trajectory into this store's runtime: it
// ensures the session's metadata entry exists on disk under the
// requested lifecycle form and rebuilds the sequence counter from the
// stored trajectory. The caller holds s.mu.
func (s *Store) attachTrajectory(key trajKey, form lifecycleForm) (*trajectory, error) {
	meta := metadataKey(key.namespace, key.session)
	var deadline time.Time
	value, err := s.eng.Get(meta)
	switch {
	case err == nil:
		if len(value) != 2 || value[0] != metadataVersion || value[1] != metadataFormPerpetual {
			return nil, fmt.Errorf("elena: attach session %q: %w", key.display(), errMalformedMetadata)
		}
	case errors.Is(err, engine.ErrNotFound):
		metaValue := encodePerpetualMetadata()
		if form.timed {
			deadline = time.Now().Add(form.ttl)
			metaValue = encodeTimedMetadata(deadline)
		}
		if err := s.putEntry("create session", meta, metaValue, deadline); err != nil {
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
// invisible together. The caller holds s.mu.
func (st *Store) putEntry(op string, key, value []byte, deadline time.Time) error {
	if deadline.IsZero() {
		if err := st.eng.Put(key, value); err != nil {
			return opErr(op, err)
		}
		return nil
	}
	if err := st.eng.PutWithTTL(key, value, time.Until(deadline)); err != nil {
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
// the moment Append returns.
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
	if traj.lastSeq == math.MaxUint64 {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), errSequenceExhausted)
	}
	if uint64(len(payload)) > math.MaxUint32 {
		return 0, fmt.Errorf("elena: append to %q: %w", sess.key.display(), errPayloadTooLarge)
	}

	seq := traj.lastSeq + 1
	key := eventKey(sess.key.namespace, sess.key.session, seq)
	value := encodeEnvelope(seq, time.Now(), payload)
	if err := st.putEntry("append event", key, value, traj.deadline); err != nil {
		return 0, err
	}
	traj.lastSeq = seq
	return seq, nil
}

package semantic

import (
	"errors"
	"fmt"
	"time"

	"github.com/magician336/elena/internal/engine"
)

// ErrEventNotFound is returned by Event when the trajectory holds no
// event with the requested sequence number.
var ErrEventNotFound = errors.New("elena: event not found")

// Event is one stored trajectory event, self-describing: the sequence
// number assigned at append time and the append time travel with the
// payload, so a read event can be displayed without external
// bookkeeping.
type Event struct {
	// Seq is the event's sequence number, starting at 1.
	Seq uint64

	// AppendedAt is when the event was appended.
	AppendedAt time.Time

	// Payload is the caller-supplied bytes, stored and returned
	// unmodified.
	Payload []byte
}

// Events returns the whole trajectory in sequence order. The read
// observes one instant: events appended after it starts are not
// included.
func (sess *Session) Events() ([]Event, error) {
	return scanEvents(sess.store.eng, sess.key, nil)
}

// EventsUpTo returns every event of the trajectory with a sequence
// number of at most seq, in sequence order — the endpoint event included.
// A seq below the trajectory's first sequence number reads empty; a seq
// beyond the stored tail reads the whole trajectory. The read
// observes one instant like the full one: it returns the events
// visible when it starts, so appends that race with it are not
// included even when their sequence numbers fall under seq.
func (sess *Session) EventsUpTo(seq uint64) ([]Event, error) {
	return scanEvents(sess.store.eng, sess.key, eventKey(sess.key.namespace, sess.key.session, seq))
}

// Event returns the single event with the given sequence number, or an
// error wrapping ErrEventNotFound when the trajectory holds no such
// event.
func (sess *Session) Event(seq uint64) (Event, error) {
	key := eventKey(sess.key.namespace, sess.key.session, seq)
	value, err := sess.store.eng.Get(key)
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			return Event{}, fmt.Errorf("elena: event %d of %q: %w", seq, sess.key.display(), ErrEventNotFound)
		}
		return Event{}, opErr("read event", err)
	}

	env, err := decodeEnvelope(value)
	if err != nil || env.sequence != seq {
		return Event{}, fmt.Errorf("elena: event %d of %q: %w", seq, sess.key.display(), errCorruptedEvent)
	}
	return Event{Seq: env.sequence, AppendedAt: env.appendedAt, Payload: env.payload}, nil
}

// scanEvents is the one read path of a trajectory, shared by the full
// read and the point-in-time read: it walks the trajectory's event keys
// in key order — which is sequence order by construction — and decodes
// each stored envelope, validating that key and envelope agree on the
// sequence number. end is the sole difference between the two reads:
// the inclusive absolute upper bound on the visited keys, given as the
// endpoint event's own encoded key — or nil for no bound, the full read.
func scanEvents(eng *engine.Engine, key trajKey, end []byte) ([]Event, error) {
	prefix := eventPrefix(key.namespace, key.session)
	var events []Event
	err := eng.Scan(engine.ScanOptions{Prefix: prefix, End: end, EndInclusive: true}, func(k, value []byte) error {
		seq, ok := sequenceFromEventKey(prefix, k)
		if !ok {
			return fmt.Errorf("elena: read trajectory of %q: %w", key.display(), errCorruptedEvent)
		}
		env, err := decodeEnvelope(value)
		if err != nil || env.sequence != seq {
			return fmt.Errorf("elena: read trajectory of %q: %w", key.display(), errCorruptedEvent)
		}
		events = append(events, Event{Seq: env.sequence, AppendedAt: env.appendedAt, Payload: env.payload})
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedEvent) {
			return nil, err
		}
		return nil, opErr("read trajectory", err)
	}
	return events, nil
}

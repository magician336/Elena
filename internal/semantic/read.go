package semantic

import (
	"errors"
	"fmt"
	"time"

	"elena/internal/engine"
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

// Events returns the whole trajectory in sequence order. The read is a
// snapshot: events appended after it starts are not included.
func (sess *Session) Events() ([]Event, error) {
	return scanEvents(sess.store.eng, sess.key)
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

// scanEvents walks the trajectory's event keys in key order — which is
// sequence order by construction — and decodes each stored envelope,
// validating that key and envelope agree on the sequence number.
func scanEvents(eng *engine.Engine, key trajKey) ([]Event, error) {
	prefix := eventPrefix(key.namespace, key.session)
	var events []Event
	err := eng.Scan(engine.ScanOptions{Prefix: prefix}, func(k, value []byte) error {
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

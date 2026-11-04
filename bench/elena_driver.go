package bench

import (
	"fmt"

	"elena"
)

// elenaNamespace is the one namespace the elena driver writes: every
// logical key becomes one trajectory inside it.
const elenaNamespace = "bench"

// lastEvent records what one key's latest append acknowledged: the
// event's sequence number and payload length, the two publicly
// observable facts a read can verify against.
type lastEvent struct {
	seq     uint64
	payload int
}

// elenaDriver drives the elena store through its public library API
// only — no internal package is importable from this module, so every
// number the harness reports for elena comes off the public surface.
//
// Mapping, the report's declared correspondence: one logical key is one
// session's trajectory (namespace "bench", session "traj-<key>"); an
// append is the trajectory's next event; a read is the single-event
// read of the trajectory's latest acknowledged event. Appends ride the
// store's default durability regime — every write synchronized on
// acknowledgment — so a completed operation is durable the moment Do
// returns.
type elenaDriver struct {
	store    *elena.Store
	sessions map[uint64]*elena.Session
	latest   map[uint64]lastEvent // key → its latest acknowledged event
}

// Name identifies the driver in reports.
func (d *elenaDriver) Name() string { return "elena" }

// Open opens a fresh store under dir with the background sweep off: no
// timed window of this harness includes background reclamation work.
func (d *elenaDriver) Open(dir string) error {
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return err
	}
	d.store = store
	d.sessions = map[uint64]*elena.Session{}
	d.latest = map[uint64]lastEvent{}
	return nil
}

// Do executes one operation: an append adds the value as its
// trajectory's next event and records what it acknowledged; a read
// resolves the key's latest event and verifies the read-back — sequence
// number and payload length must equal what the last append
// acknowledged, so a read that misses the newest event fails the run
// instead of passing silently.
func (d *elenaDriver) Do(op Op) error {
	switch op.Kind {
	case OpAppend:
		sess, err := d.session(op.Key)
		if err != nil {
			return err
		}
		seq, err := sess.Append(op.Value)
		if err != nil {
			return err
		}
		d.latest[op.Key] = lastEvent{seq: seq, payload: len(op.Value)}
		return nil
	case OpRead:
		want, ok := d.latest[op.Key]
		if !ok {
			return fmt.Errorf("read of key %d with no acknowledged append", op.Key)
		}
		sess, err := d.session(op.Key)
		if err != nil {
			return err
		}
		event, err := sess.Event(want.seq)
		if err != nil {
			return err
		}
		if event.Seq != want.seq || len(event.Payload) != want.payload {
			return fmt.Errorf("read of key %d returned seq %d payload %d bytes, want seq %d payload %d bytes",
				op.Key, event.Seq, len(event.Payload), want.seq, want.payload)
		}
		return nil
	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// Close closes the store. Operations after it surface the store's own
// closed errors — the driver adds no second bookkeeping on top.
func (d *elenaDriver) Close() error {
	return d.store.Close()
}

// session returns the trajectory handle for key, creating the session
// on first use — creating an existing perpetual session continues the
// stored trajectory.
func (d *elenaDriver) session(key uint64) (*elena.Session, error) {
	if sess, ok := d.sessions[key]; ok {
		return sess, nil
	}
	sess, err := d.store.CreateSession(elenaNamespace, elenaSessionName(key))
	if err != nil {
		return nil, err
	}
	d.sessions[key] = sess
	return sess, nil
}

// elenaSessionName renders one logical key as its trajectory's session
// name inside the bench namespace.
func elenaSessionName(key uint64) string {
	return fmt.Sprintf("traj-%d", key)
}

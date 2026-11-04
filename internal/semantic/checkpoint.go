package semantic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"elena/internal/engine"
)

// The checkpoint manifest: the value bytes stored for one checkpoint.
// It is a manifest of the whole store — for every live session, the
// trajectory's reached sequence number and the session's deadline — plus
// the checkpoint's own identity: its ID travels in the key, its creation
// time leads the value.
//
//	version 1 byte | created-at 8 bytes | count 4 bytes | entry*
//
// One entry per session, in scan order:
//
//	len(namespace) 2 | namespace | len(session) 2 | session | last-seq 8 | form 1 | [deadline 8]
//
// All integers are big-endian; times are Unix nanoseconds. The labels
// reuse the length-prefixed encoding of the key space; the form byte
// reuses the metadata lifecycle vocabulary — perpetual alone, timed
// followed by its deadline.
const (
	// manifestVersion is the manifest layout version.
	manifestVersion byte = 0x01

	// manifestFixedSize is the byte size before the entries: version,
	// creation time, entry count.
	manifestFixedSize = 1 + 8 + 4

	// manifestEntryFixedSize is the fixed byte size of one entry before
	// the two labels: last-seq and form.
	manifestEntryFixedSize = 8 + 1
)

// errCorruptedCheckpoint marks stored checkpoint bytes or keys that fail
// their structural validation at read time.
var errCorruptedCheckpoint = errors.New("elena: corrupted checkpoint")

// ErrCheckpointNotFound is returned by Restore when the store holds no
// checkpoint with the requested ID.
var ErrCheckpointNotFound = errors.New("elena: checkpoint not found")

// checkpointEntry is one session's line of the manifest: the trajectory
// the entry names, the last sequence number the checkpoint reached, and
// the session's deadline — the zero time marks a perpetual session.
type checkpointEntry struct {
	key      trajKey
	lastSeq  uint64
	deadline time.Time
}

// checkpointManifest is the decoded form of a checkpoint's stored value.
type checkpointManifest struct {
	createdAt time.Time
	entries   []checkpointEntry
}

// encodeManifest returns the stored value of a manifest. The entries are
// written in the caller's order; CreateCheckpoint passes scan order, so
// the encoding is deterministic for one store state.
func encodeManifest(m checkpointManifest) []byte {
	value := make([]byte, manifestFixedSize, manifestFixedSize+len(m.entries)*32)
	value[0] = manifestVersion
	binary.BigEndian.PutUint64(value[1:], uint64(m.createdAt.UnixNano()))
	binary.BigEndian.PutUint32(value[9:], uint32(len(m.entries)))
	for _, entry := range m.entries {
		value = appendLabel(value, entry.key.namespace)
		value = appendLabel(value, entry.key.session)
		var lastSeq [8]byte
		binary.BigEndian.PutUint64(lastSeq[:], entry.lastSeq)
		value = append(value, lastSeq[:]...)
		if entry.deadline.IsZero() {
			value = append(value, metadataFormPerpetual)
			continue
		}
		value = append(value, metadataFormTimed)
		var deadline [8]byte
		binary.BigEndian.PutUint64(deadline[:], uint64(entry.deadline.UnixNano()))
		value = append(value, deadline[:]...)
	}
	return value
}

// decodeManifest validates and decodes a checkpoint's stored value.
func decodeManifest(value []byte) (checkpointManifest, error) {
	if len(value) < manifestFixedSize || value[0] != manifestVersion {
		return checkpointManifest{}, errCorruptedCheckpoint
	}
	createdAt := time.Unix(0, int64(binary.BigEndian.Uint64(value[1:])))
	if createdAt.IsZero() {
		return checkpointManifest{}, errCorruptedCheckpoint // a checkpoint is created at a real instant
	}
	m := checkpointManifest{createdAt: createdAt}
	count := int(binary.BigEndian.Uint32(value[9:]))
	rest := value[manifestFixedSize:]
	for i := 0; i < count; i++ {
		var entry checkpointEntry
		nsLen, ns, ok := takeLabel(rest)
		if !ok {
			return checkpointManifest{}, errCorruptedCheckpoint
		}
		rest = rest[2+nsLen:]
		idLen, id, ok := takeLabel(rest)
		if !ok {
			return checkpointManifest{}, errCorruptedCheckpoint
		}
		rest = rest[2+idLen:]
		if len(rest) < manifestEntryFixedSize {
			return checkpointManifest{}, errCorruptedCheckpoint
		}
		entry.key = trajKey{namespace: ns, session: id}
		entry.lastSeq = binary.BigEndian.Uint64(rest[:8])
		switch form := rest[8]; form {
		case metadataFormPerpetual:
			rest = rest[9:]
		case metadataFormTimed:
			rest = rest[9:]
			if len(rest) < 8 {
				return checkpointManifest{}, errCorruptedCheckpoint
			}
			deadline := time.Unix(0, int64(binary.BigEndian.Uint64(rest[:8])))
			if deadline.IsZero() {
				return checkpointManifest{}, errCorruptedCheckpoint // the zero deadline belongs to the perpetual form alone
			}
			entry.deadline = deadline
			rest = rest[8:]
		default:
			return checkpointManifest{}, errCorruptedCheckpoint
		}
		m.entries = append(m.entries, entry)
	}
	if len(rest) != 0 {
		return checkpointManifest{}, errCorruptedCheckpoint
	}
	return m, nil
}

// takeLabel decodes one length-prefixed label off the front of src,
// reporting whether the framing is well-formed.
func takeLabel(src []byte) (length int, label string, ok bool) {
	if len(src) < 2 {
		return 0, "", false
	}
	length = int(binary.BigEndian.Uint16(src[:2]))
	if len(src) < 2+length {
		return 0, "", false
	}
	return length, string(src[2 : 2+length]), true
}

// CheckpointInfo describes one stored checkpoint: its ID and when it
// was created.
type CheckpointInfo struct {
	// ID identifies the checkpoint; Restore takes it back.
	ID uint64

	// CreatedAt is when the checkpoint was created.
	CreatedAt time.Time
}

// CreateCheckpoint writes a manifest of the whole store — every live
// session's trajectory with its reached sequence number and its deadline
// — as one checkpoint, durably stored in a single engine batch. It
// returns the checkpoint's ID. The manifest is ordinary stored data: it
// can be listed and restored across Close and Open.
func (s *Store) CreateCheckpoint() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	manifest, err := s.buildManifest()
	if err != nil {
		return 0, err
	}
	id, err := s.nextCheckpointIDLocked()
	if err != nil {
		return 0, err
	}
	batch := s.eng.NewBatch()
	if err := batch.Put(checkpointKey(id), encodeManifest(manifest)); err != nil {
		return 0, opErr("create checkpoint", err)
	}
	if err := batch.Commit(); err != nil {
		return 0, opErr("create checkpoint", err)
	}
	return id, nil
}

// Checkpoints returns every stored checkpoint in creation order — which
// is ID order by construction of the key encoding.
func (s *Store) Checkpoints() ([]CheckpointInfo, error) {
	var checkpoints []CheckpointInfo
	err := s.eng.Scan(engine.ScanOptions{Prefix: []byte{keyAreaCheckpoint}}, func(k, value []byte) error {
		id, ok := checkpointIDFromKey(k)
		if !ok {
			return fmt.Errorf("elena: list checkpoints: %w", errCorruptedCheckpoint)
		}
		manifest, err := decodeManifest(value)
		if err != nil {
			return fmt.Errorf("elena: list checkpoints: %w", err)
		}
		checkpoints = append(checkpoints, CheckpointInfo{ID: id, CreatedAt: manifest.createdAt})
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedCheckpoint) {
			return nil, err
		}
		return nil, opErr("list checkpoints", err)
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].ID < checkpoints[j].ID })
	return checkpoints, nil
}

// Restore resets the store's runtime view — every trajectory the
// checkpoint records gets back its recorded sequence number and
// deadline — to the values the checkpoint's manifest wrote down.
// Sessions attached to this store that the checkpoint does not record
// keep their current runtime state. The manifest's deadline for a timed
// session is the deadline its stored entries have carried all along, so
// a restored session elapses at its original deadline, not one
// recomputed at restore time.
func (s *Store) Restore(id uint64) error {
	value, err := s.eng.Get(checkpointKey(id))
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			return fmt.Errorf("elena: restore checkpoint %d: %w", id, ErrCheckpointNotFound)
		}
		return opErr("restore checkpoint", err)
	}
	manifest, err := decodeManifest(value)
	if err != nil {
		return fmt.Errorf("elena: restore checkpoint %d: %w", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	restored := make(map[trajKey]*trajectory, len(manifest.entries)+len(s.trajectories))
	for _, entry := range manifest.entries {
		restored[entry.key] = &trajectory{lastSeq: entry.lastSeq, deadline: entry.deadline}
	}
	// Sessions the checkpoint does not record are data after the
	// baseline: they keep their runtime state untouched.
	for key, traj := range s.trajectories {
		if _, ok := restored[key]; !ok {
			restored[key] = traj
		}
	}
	s.trajectories = restored
	return nil
}

// buildManifest collects the whole store's manifest: every live session
// found on disk, with each trajectory's reached sequence number — the
// greatest of the stored tail and this store's runtime counter, so
// sequence numbers allocated to a committing batch are recorded too —
// and each session's deadline. The caller holds s.mu.
func (s *Store) buildManifest() (checkpointManifest, error) {
	var manifest checkpointManifest
	err := s.eng.Scan(engine.ScanOptions{Prefix: []byte{keyAreaSession}}, func(k, value []byte) error {
		namespace, session, kind, ok := parseSessionKey(k)
		if !ok {
			return fmt.Errorf("elena: checkpoint manifest: %w", errCorruptedSessionKey)
		}
		if kind != keyKindMetadata {
			return nil
		}
		_, deadline, derr := decodeMetadata(value)
		if derr != nil {
			return fmt.Errorf("elena: checkpoint manifest: %w", derr)
		}
		key := trajKey{namespace: namespace, session: session}
		lastSeq, err := s.reachedSequence(key)
		if err != nil {
			return err
		}
		manifest.entries = append(manifest.entries, checkpointEntry{key: key, lastSeq: lastSeq, deadline: deadline})
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedSessionKey) || errors.Is(err, errMalformedMetadata) || errors.Is(err, errCorruptedEvent) {
			return checkpointManifest{}, err
		}
		return checkpointManifest{}, opErr("create checkpoint", err)
	}
	// The engine's scan order — key byte order — is already the
	// deterministic entry order of the encoding.
	manifest.createdAt = time.Now()
	return manifest, nil
}

// reachedSequence returns the sequence number a checkpoint records for
// one trajectory: the greatest of the stored tail and this store's
// runtime counter, so a manifest written while a batch is committing
// records the sequence numbers the batch allocated. The runtime counter
// never trails the stored tail. The caller holds s.mu.
func (s *Store) reachedSequence(key trajKey) (uint64, error) {
	last := uint64(0)
	if traj, ok := s.trajectories[key]; ok {
		last = traj.lastSeq
	}
	stored, err := s.lastSequence(key)
	if err != nil {
		return 0, err
	}
	return max(last, stored), nil
}

// nextCheckpointIDLocked returns the ID the next checkpoint takes: one
// past the highest stored ID. The caller holds s.mu, so concurrent
// checkpoint creations are serialized and every ID handed out lands in
// creation order.
func (s *Store) nextCheckpointIDLocked() (uint64, error) {
	var highest uint64
	err := s.eng.Scan(engine.ScanOptions{Prefix: []byte{keyAreaCheckpoint}}, func(k, _ []byte) error {
		id, ok := checkpointIDFromKey(k)
		if !ok {
			return fmt.Errorf("elena: create checkpoint: %w", errCorruptedCheckpoint)
		}
		highest = max(highest, id)
		return nil
	})
	if err != nil {
		if errors.Is(err, errCorruptedCheckpoint) {
			return 0, err
		}
		return 0, opErr("create checkpoint", err)
	}
	if highest == math.MaxUint64 {
		return 0, fmt.Errorf("elena: create checkpoint: checkpoint IDs exhausted")
	}
	return highest + 1, nil
}

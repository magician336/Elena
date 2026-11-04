package semantic

import (
	"encoding/binary"
	"errors"
)

// The key space of the semantic layer. Every key starts with an area
// byte that separates record classes, followed by the length-prefixed
// namespace and session ID, followed by a kind byte:
//
//	0x01 | len(namespace) | namespace | len(session) | session | kind | ...
//
// kind 0x00 introduces the session's metadata entry; kind 0x01 the
// trajectory's event entries, which end in the fixed-width big-endian
// sequence number. This layout is a stability contract: later features
// extend it, they never break it.
//
// Two properties do the load-bearing work. The two-byte length
// prefixes make the label encodings prefix-free, so no session's keys
// can ever collide with or nest inside another's, whatever the labels
// contain. And the fixed-width big-endian sequence number makes byte
// order equal numeric order equal append order, so a scan in key order
// returns a trajectory in happening order.
const (
	// keyAreaSession marks every key belonging to one session: its
	// metadata entry and its trajectory's event entries.
	keyAreaSession byte = 0x01

	// keyKindMetadata marks the session's metadata entry.
	keyKindMetadata byte = 0x00

	// keyKindEvent marks one trajectory event.
	keyKindEvent byte = 0x01

	// keyAreaCheckpoint marks one checkpoint manifest. It extends the
	// key space with a second record class; the session class above is
	// untouched.
	keyAreaCheckpoint byte = 0x02

	// sequenceWidth is the fixed width of the encoded sequence number.
	sequenceWidth = 8

	// checkpointIDWidth is the fixed width of the encoded checkpoint ID.
	checkpointIDWidth = 8
)

// maxLabelBytes bounds the namespace and the session ID individually.
// The encoded event key of two maximal labels plus the fixed framing
// stays inside the engine's default key limit, so the engine never has
// to reject a key the semantic layer accepted.
const maxLabelBytes = 480

// appendLabel appends the encoding of label — a two-byte big-endian
// byte length followed by the bytes — to dst.
func appendLabel(dst []byte, label string) []byte {
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(label)))
	dst = append(dst, length[:]...)
	return append(dst, label...)
}

// sessionPrefix returns the key prefix shared by everything stored for
// one session: its metadata entry and its trajectory's events.
func sessionPrefix(namespace, session string) []byte {
	prefix := make([]byte, 0, 1+2+len(namespace)+2+len(session))
	prefix = append(prefix, keyAreaSession)
	prefix = appendLabel(prefix, namespace)
	prefix = appendLabel(prefix, session)
	return prefix
}

// metadataKey returns the key of the session's metadata entry.
func metadataKey(namespace, session string) []byte {
	return append(sessionPrefix(namespace, session), keyKindMetadata)
}

// eventPrefix returns the key prefix of the session's trajectory: an
// event's key is this prefix plus its encoded sequence number.
func eventPrefix(namespace, session string) []byte {
	return append(sessionPrefix(namespace, session), keyKindEvent)
}

// eventKey returns the key of the event with the given sequence number.
func eventKey(namespace, session string, seq uint64) []byte {
	prefix := eventPrefix(namespace, session)
	key := make([]byte, len(prefix)+sequenceWidth)
	copy(key, prefix)
	binary.BigEndian.PutUint64(key[len(prefix):], seq)
	return key
}

// sequenceFromEventKey decodes the sequence number out of an event key
// carrying the given trajectory prefix, reporting whether the key is
// well-formed.
func sequenceFromEventKey(prefix, key []byte) (uint64, bool) {
	if len(key) != len(prefix)+sequenceWidth {
		return 0, false
	}
	return binary.BigEndian.Uint64(key[len(key)-sequenceWidth:]), true
}

// namespacePrefix returns the key prefix shared by every entry of one
// namespace's sessions. The length-prefixed label encoding keeps it free
// of every other namespace's keys, nested or not, whatever the labels
// contain.
func namespacePrefix(namespace string) []byte {
	prefix := make([]byte, 0, 1+2+len(namespace))
	prefix = append(prefix, keyAreaSession)
	prefix = appendLabel(prefix, namespace)
	return prefix
}

// checkpointKey returns the key of one checkpoint manifest: the
// checkpoint area byte followed by the fixed-width big-endian checkpoint
// ID. The fixed-width big-endian encoding keeps byte order equal ID
// order equal creation order, so a scan of the checkpoint area lists
// checkpoints in the order they were created.
func checkpointKey(id uint64) []byte {
	key := make([]byte, 1+checkpointIDWidth)
	key[0] = keyAreaCheckpoint
	binary.BigEndian.PutUint64(key[1:], id)
	return key
}

// checkpointIDFromKey decodes the ID out of a checkpoint-area key,
// reporting whether the key is well-formed.
func checkpointIDFromKey(key []byte) (uint64, bool) {
	if len(key) != 1+checkpointIDWidth || key[0] != keyAreaCheckpoint {
		return 0, false
	}
	return binary.BigEndian.Uint64(key[1:]), true
}

// parseSessionKey splits a session-area key into its namespace, session
// ID, and kind byte, reporting whether the framing is well-formed. A
// kind this layout does not define yet parses as well-formed; the bytes
// after such a kind are that kind's to define.
func parseSessionKey(key []byte) (namespace, session string, kind byte, ok bool) {
	// 0x01 | len(namespace) | namespace | len(session) | session | kind
	if len(key) < 1+2+2+1 || key[0] != keyAreaSession {
		return "", "", 0, false
	}
	nsLen := int(binary.BigEndian.Uint16(key[1:3]))
	if len(key) < 3+nsLen+2+1 {
		return "", "", 0, false
	}
	namespace = string(key[3 : 3+nsLen])
	idOff := 3 + nsLen
	idLen := int(binary.BigEndian.Uint16(key[idOff : idOff+2]))
	idOff += 2
	if len(key) < idOff+idLen+1 {
		return "", "", 0, false
	}
	session = string(key[idOff : idOff+idLen])
	kind = key[idOff+idLen]
	switch rest := len(key) - (idOff + idLen + 1); kind {
	case keyKindMetadata:
		if rest != 0 {
			return "", "", 0, false
		}
	case keyKindEvent:
		if rest != sequenceWidth {
			return "", "", 0, false
		}
	}
	return namespace, session, kind, true
}

// errCorruptedSessionKey marks a stored session-area key that fails its
// structural validation at read time.
var errCorruptedSessionKey = errors.New("elena: corrupted session key")

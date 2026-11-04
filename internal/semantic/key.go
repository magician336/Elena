package semantic

import "encoding/binary"

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

	// sequenceWidth is the fixed width of the encoded sequence number.
	sequenceWidth = 8
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

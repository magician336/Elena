package semantic

import (
	"encoding/binary"
	"errors"
	"time"
)

// The event envelope: the value bytes stored for one trajectory event.
// It makes a read event self-describing — the sequence number and the
// append time can be displayed without external bookkeeping — while
// the payload stays opaque bytes to the store.
//
//	version 1 byte | sequence 8 bytes | appended-at 8 bytes | payload length 4 bytes | payload
//
// All integers are big-endian; appended-at is Unix nanoseconds.
const (
	// envelopeVersion is the envelope layout version.
	envelopeVersion byte = 0x01

	// envelopeHeaderSize is the fixed byte size preceding the payload.
	envelopeHeaderSize = 1 + 8 + 8 + 4
)

// errCorruptedEvent marks stored trajectory bytes that fail key or
// envelope validation at read time.
var errCorruptedEvent = errors.New("elena: corrupted trajectory event")

// envelope is the decoded form of an event's stored value bytes.
type envelope struct {
	sequence   uint64
	appendedAt time.Time
	payload    []byte
}

// encodeEnvelope returns the envelope for the event with the given
// sequence number, append time, and payload. The payload is copied.
func encodeEnvelope(seq uint64, appendedAt time.Time, payload []byte) []byte {
	value := make([]byte, envelopeHeaderSize+len(payload))
	value[0] = envelopeVersion
	binary.BigEndian.PutUint64(value[1:], seq)
	binary.BigEndian.PutUint64(value[9:], uint64(appendedAt.UnixNano()))
	binary.BigEndian.PutUint32(value[17:], uint32(len(payload)))
	copy(value[envelopeHeaderSize:], payload)
	return value
}

// decodeEnvelope validates and decodes an event's stored value bytes,
// reporting errCorruptedEvent for anything that is not an envelope of
// the known version with a consistent payload length. The payload is
// copied, so the caller may reuse the value slice.
func decodeEnvelope(value []byte) (envelope, error) {
	if len(value) < envelopeHeaderSize || value[0] != envelopeVersion {
		return envelope{}, errCorruptedEvent
	}
	sequence := binary.BigEndian.Uint64(value[1:])
	appendedAt := time.Unix(0, int64(binary.BigEndian.Uint64(value[9:])))
	payloadLen := binary.BigEndian.Uint32(value[17:])
	if uint64(envelopeHeaderSize)+uint64(payloadLen) != uint64(len(value)) {
		return envelope{}, errCorruptedEvent
	}
	payload := make([]byte, payloadLen)
	copy(payload, value[envelopeHeaderSize:])
	return envelope{sequence: sequence, appendedAt: appendedAt, payload: payload}, nil
}

package engine

import (
	"encoding/binary"
	"hash/crc32"
	"math"
)

// On-disk entry layout (all integers little-endian):
//
//	+--------+-------------+-------------+------------+-------------+
//	| crc 4B | timestamp 8B| expires 8B  | keyLen 4B  | valueLen 4B |
//	+--------+-------------+-------------+------------+-------------+
//	| key (keyLen bytes)   | value (valueLen bytes)                  |
//	+---------------------------------------------------------------+
//
// The header is entryHeaderSize bytes. The CRC-32 (IEEE) covers the
// whole entry excluding the CRC field itself, so a torn tail write is
// always detectable. A tombstone (delete) is encoded as
// valueLen == tombstoneValueLen with no value bytes. The expires field
// is 0 for entries that never expire; TTL read semantics arrive in a
// later ticket but the field is part of the on-disk format from day
// one so later tickets add logic without changing the layout.
const (
	entryHeaderSize   = 4 + 8 + 8 + 4 + 4
	tombstoneValueLen = math.MaxUint32

	headerTimestampOff = 4
	headerExpiresOff   = 12
	headerKeyLenOff    = 20
	headerValueLenOff  = 24
)

// entryHeader is the decoded fixed-size prefix of an entry.
type entryHeader struct {
	crc       uint32
	timestamp int64
	expires   int64
	keyLen    uint32
	valueLen  uint32
}

// appendValueEntry appends the wire encoding of a Put entry to buf and
// returns the extended slice.
func appendValueEntry(buf []byte, timestamp, expires int64, key, value []byte) []byte {
	return appendEntry(buf, timestamp, expires, key, value, uint32(len(value)))
}

// appendTombstoneEntry appends the wire encoding of a Delete entry to
// buf and returns the extended slice.
func appendTombstoneEntry(buf []byte, timestamp int64, key []byte) []byte {
	return appendEntry(buf, timestamp, 0, key, nil, tombstoneValueLen)
}

// appendEntry appends one entry with the given header fields and body.
// valueLen is the encoded length field: len(value) for puts and
// tombstoneValueLen for deletes.
func appendEntry(buf []byte, timestamp, expires int64, key, value []byte, valueLen uint32) []byte {
	start := len(buf)
	buf = append(buf, make([]byte, entryHeaderSize)...)
	buf = append(buf, key...)
	buf = append(buf, value...)

	binary.LittleEndian.PutUint64(buf[start+headerTimestampOff:], uint64(timestamp))
	binary.LittleEndian.PutUint64(buf[start+headerExpiresOff:], uint64(expires))
	binary.LittleEndian.PutUint32(buf[start+headerKeyLenOff:], uint32(len(key)))
	binary.LittleEndian.PutUint32(buf[start+headerValueLenOff:], valueLen)
	binary.LittleEndian.PutUint32(buf[start:], entryCRC(buf[start:]))
	return buf
}

// decodeHeader parses the fixed-size entry header from the first
// entryHeaderSize bytes of an entry.
func decodeHeader(b []byte) entryHeader {
	return entryHeader{
		crc:       binary.LittleEndian.Uint32(b),
		timestamp: int64(binary.LittleEndian.Uint64(b[headerTimestampOff:])),
		expires:   int64(binary.LittleEndian.Uint64(b[headerExpiresOff:])),
		keyLen:    binary.LittleEndian.Uint32(b[headerKeyLenOff:]),
		valueLen:  binary.LittleEndian.Uint32(b[headerValueLenOff:]),
	}
}

// entryCRC computes the checksum of a complete encoded entry. The CRC
// covers everything after the leading CRC field: the rest of the
// header plus key and value.
func entryCRC(entry []byte) uint32 {
	return crc32.ChecksumIEEE(entry[4:])
}

// valueBounds returns the [start, end) bounds of the value inside a
// complete encoded entry.
func valueBounds(h entryHeader) (start, end int) {
	start = entryHeaderSize + int(h.keyLen)
	if h.valueLen == tombstoneValueLen {
		return start, start
	}
	return start, start + int(h.valueLen)
}

// expiredAt reports whether the entry is dead at the given instant
// (Unix nanoseconds). expires == 0 means the entry never expires; an
// entry is expired from the instant its deadline is reached onward.
func (h entryHeader) expiredAt(now int64) bool {
	return h.expires != 0 && now >= h.expires
}

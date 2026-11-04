package recovery

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// payloadAt derives event e's payload from the spec alone: SHA-256 in
// counter mode over (seed, event, block), truncated to PayloadSize.
// Deriving per event — not from one long stream — is what lets the
// parent verify any acknowledged event at any dataset scale without
// holding the whole dataset in memory: child and parent compute the
// same bytes from the same (seed, e) pair, so a corrupted payload can
// never be explained away as "the child wrote something else".
func (s Spec) payloadAt(e int) []byte {
	var head [24]byte
	binary.LittleEndian.PutUint64(head[0:8], uint64(s.Seed))
	binary.LittleEndian.PutUint64(head[8:16], uint64(e))
	payload := make([]byte, 0, s.PayloadSize)
	for block := uint64(0); len(payload) < s.PayloadSize; block++ {
		binary.LittleEndian.PutUint64(head[16:24], block)
		sum := sha256.Sum256(head[:])
		payload = append(payload, sum[:]...)
	}
	return payload[:s.PayloadSize]
}

// Ack is one acknowledgment line of the child's protocol: the session
// index and sequence number of an append that has returned — durable
// the moment the parent reads the line, because the child rides the
// store's default write-synchronized regime.
type Ack struct {
	Session int
	Seq     uint64
}

// AckStream returns the predicted acknowledgment stream for spec, in
// order: one ack per schedule event — event e acknowledged as session
// e%Sessions, sequence e/Sessions+1. Parent and child share this
// arithmetic, so the parent verifies every line without trusting the
// child beyond the ordering.
func AckStream(spec Spec) []Ack {
	stream := make([]Ack, spec.Events)
	for e := range stream {
		stream[e] = Ack{Session: e % spec.Sessions, Seq: uint64(e/spec.Sessions + 1)}
	}
	return stream
}

// parseAckLine decodes one 'A' line: "A <session> <sequence>".
// Anything else is an error — the protocol has no other vocabulary.
func parseAckLine(line string) (Ack, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != "A" {
		return Ack{}, fmt.Errorf("malformed ack line %q", line)
	}
	session, err := strconv.Atoi(fields[1])
	if err != nil || session < 0 {
		return Ack{}, fmt.Errorf("malformed ack line %q: bad session", line)
	}
	seq, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return Ack{}, fmt.Errorf("malformed ack line %q: bad sequence", line)
	}
	return Ack{Session: session, Seq: seq}, nil
}

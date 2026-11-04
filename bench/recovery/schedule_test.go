package recovery

import (
	"bytes"
	"testing"
)

// TestPayloadAt pins the payload derivation on both sides of the
// process boundary: exact length, byte-stable across calls and across
// spec instances, distinct per event and per seed.
func TestPayloadAt(t *testing.T) {
	spec := validSpec()
	first := spec.payloadAt(0)
	mid := spec.payloadAt(32)
	last := spec.payloadAt(63)
	for name, payload := range map[string][]byte{"first": first, "mid": mid, "last": last} {
		if len(payload) != spec.PayloadSize {
			t.Fatalf("payload of %s event = %d bytes, want %d", name, len(payload), spec.PayloadSize)
		}
	}
	if !bytes.Equal(first, spec.payloadAt(0)) {
		t.Fatal("payload of event 0 changed between calls — derivation is not deterministic")
	}
	fresh := validSpec() // a second instance must derive the same bytes
	if !bytes.Equal(mid, fresh.payloadAt(32)) {
		t.Fatal("two spec instances derived different payloads for event 32")
	}
	if bytes.Equal(first, mid) || bytes.Equal(mid, last) {
		t.Fatal("distinct events derived equal payloads")
	}
	other := validSpec()
	other.Seed = 2
	if bytes.Equal(first, other.payloadAt(0)) {
		t.Fatal("distinct seeds derived equal payloads for event 0")
	}
}

// TestPayloadAtAllSizes pins that arbitrary payload lengths — not just
// whole SHA-256 blocks — come out at the exact requested length.
func TestPayloadAtAllSizes(t *testing.T) {
	for _, size := range []int{1, 31, 32, 33, 64, 65, 512} {
		spec := validSpec()
		spec.PayloadSize = size
		if payload := spec.payloadAt(5); len(payload) != size {
			t.Fatalf("payload size %d derived %d bytes", size, len(payload))
		}
	}
}

// TestAckStream pins the predicted acknowledgment stream: one 'A' ack
// per event in schedule order — event e acknowledged as session
// e%Sessions, sequence e/Sessions+1 — with nothing else in the stream.
func TestAckStream(t *testing.T) {
	spec := validSpec() // 64 events over 4 sessions
	stream := AckStream(spec)
	if len(stream) != spec.Events {
		t.Fatalf("AckStream holds %d acks, want %d", len(stream), spec.Events)
	}
	for e, ack := range stream {
		want := Ack{Session: e % spec.Sessions, Seq: uint64(e/spec.Sessions + 1)}
		if ack != want {
			t.Fatalf("AckStream[%d] = %+v, want %+v", e, ack, want)
		}
	}
	// One session's view: sequences arrive strictly increasing.
	last := uint64(0)
	for _, ack := range stream {
		if ack.Session == 0 {
			if ack.Seq <= last {
				t.Fatalf("session 0 sequences not increasing: %d after %d", ack.Seq, last)
			}
			last = ack.Seq
		}
	}
}

// TestParseAckLine pins the line protocol's whole vocabulary: the 'A'
// line parses, and every malformed shape is an error.
func TestParseAckLine(t *testing.T) {
	ack, err := parseAckLine("A 2 17")
	if err != nil || ack != (Ack{Session: 2, Seq: 17}) {
		t.Fatalf("parseAckLine(%q) = %+v, %v", "A 2 17", ack, err)
	}
	for _, line := range []string{
		"", "A", "A 2", "A 2 17 9", "A x 17", "A 2 x", "B 2 17", "a 2 17", "A -1 17",
	} {
		if _, err := parseAckLine(line); err == nil {
			t.Fatalf("parseAckLine(%q) accepted a malformed line", line)
		}
	}
}

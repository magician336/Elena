package engine_test

import (
	"errors"
	"testing"

	"elena/internal/engine"
)

// A key written through Put must be readable back through Get with the
// exact same value: this is the smallest end-to-end heartbeat of the
// write path (append + fsync) and the read path (index + file read).
func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	const key = "session-42"
	const value = "checkpoint blob"

	if err := eng.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q) failed: %v", key, err)
	}

	got, err := eng.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q) failed: %v", key, err)
	}
	if string(got) != value {
		t.Errorf("Get(%q) = %q, want %q", key, got, value)
	}
}

// Get on a key that was never written must report ErrNotFound rather
// than an empty value or a generic error.
func TestGetMissingKeyReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	got, err := eng.Get([]byte("never-written"))
	if !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Errorf("Get(missing) value = %q, want nil", got)
	}
}

// Table-driven round trip over assorted key and value shapes (empty
// value, binary bytes) so encoding edge cases stay covered.
func TestPutGetAssortedValues(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	tests := []struct {
		name  string
		key   string
		value []byte
	}{
		{"empty value", "empty", []byte{}},
		{"binary value", "binary", []byte{0x00, 0xff, 0x0a, 0x1b, 0x00, 0x7f}},
		{"unicode key and value", "键/trajectory", []byte("值·チェックポイント")},
		{"long value", "long", bytesOfLength(4096)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := eng.Put([]byte(tt.key), tt.value); err != nil {
				t.Fatalf("Put(%q) failed: %v", tt.key, err)
			}
			got, err := eng.Get([]byte(tt.key))
			if err != nil {
				t.Fatalf("Get(%q) failed: %v", tt.key, err)
			}
			if string(got) != string(tt.value) {
				t.Errorf("Get(%q) = %q, want %q", tt.key, got, tt.value)
			}
		})
	}
}

// bytesOfLength returns a deterministic pseudo-random byte slice of the
// requested length (fixture helper, not a engine behavior).
func bytesOfLength(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/251)
	}
	return b
}

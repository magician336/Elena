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

// Put must reject keys and values over the configured limits with
// distinct sentinel errors, so upper layers can rely on a hard
// boundary instead of unbounded memory growth. Default limits apply
// when Options leaves them unset; explicit smaller limits are honored.
func TestPutRejectsOversizedKeyAndValue(t *testing.T) {
	tests := []struct {
		name    string
		opts    engine.Options
		key     []byte
		value   []byte
		wantErr error
	}{
		{
			name:    "default key limit",
			opts:    engine.Options{},
			key:     bytesOfLength(1024 + 1),
			value:   []byte("v"),
			wantErr: engine.ErrKeyTooLarge,
		},
		{
			name:    "default value limit",
			opts:    engine.Options{},
			key:     []byte("k"),
			value:   bytesOfLength(4<<20 + 1),
			wantErr: engine.ErrValueTooLarge,
		},
		{
			name:    "custom smaller key limit",
			opts:    engine.Options{MaxKeySize: 8},
			key:     bytesOfLength(9),
			value:   []byte("v"),
			wantErr: engine.ErrKeyTooLarge,
		},
		{
			name:    "custom smaller value limit",
			opts:    engine.Options{MaxValueSize: 8},
			key:     []byte("k"),
			value:   bytesOfLength(9),
			wantErr: engine.ErrValueTooLarge,
		},
		{
			name:    "key at exactly the limit is accepted",
			opts:    engine.Options{MaxKeySize: 8},
			key:     bytesOfLength(8),
			value:   []byte("v"),
			wantErr: nil,
		},
		{
			name:    "value at exactly the limit is accepted",
			opts:    engine.Options{MaxValueSize: 8},
			key:     []byte("k"),
			value:   bytesOfLength(8),
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := engine.Open(t.TempDir(), tt.opts)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			defer closeEngine(t, eng)

			err = eng.Put(tt.key, tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Put err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			got, err := eng.Get(tt.key)
			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}
			if string(got) != string(tt.value) {
				t.Errorf("Get = %q, want %q", got, tt.value)
			}
		})
	}
}

// A rejected oversized write must leave no trace: the key stays
// unreadable and nothing durable is appended.
func TestPutOversizedLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{MaxValueSize: 8})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer closeEngine(t, eng)

	if err := eng.Put([]byte("k"), bytesOfLength(9)); !errors.Is(err, engine.ErrValueTooLarge) {
		t.Fatalf("Put oversized err = %v, want ErrValueTooLarge", err)
	}
	if _, err := eng.Get([]byte("k")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after rejected Put err = %v, want ErrNotFound", err)
	}
}

//go:build unix

package engine_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"elena/internal/engine"
)

// shortTTL is long enough to stay comfortably unexpired across an
// immediate Get, and expiryWait is a generous multiple of it so the
// "expired" assertions never race the clock.
const (
	shortTTL  = 80 * time.Millisecond
	expiryWait = 200 * time.Millisecond
)

// A value written with a TTL must be readable while the TTL is still
// live: the read path returns the exact value as with a plain Put.
func TestPutWithTTLValueReadableBeforeExpiry(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	if err := eng.PutWithTTL([]byte("session-42"), []byte("checkpoint blob"), 10*time.Second); err != nil {
		t.Fatalf("PutWithTTL failed: %v", err)
	}

	got, err := eng.Get([]byte("session-42"))
	if err != nil {
		t.Fatalf("Get after PutWithTTL failed: %v", err)
	}
	if string(got) != "checkpoint blob" {
		t.Errorf("Get after PutWithTTL = %q, want %q", got, "checkpoint blob")
	}
}

// PutWithTTL must reject ttls that carry no honest meaning: zero and
// negative ones (born-expired entries) and ones so large the expiry
// timestamp would overflow the entry header's int64 nanoseconds.
func TestPutWithTTLRejectsInvalidTTL(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero ttl", 0},
		{"negative ttl", -time.Second},
		{"unrepresentable ttl", math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "ttl-" + tt.name
			err := eng.PutWithTTL([]byte(key), []byte("v"), tt.ttl)
			if !errors.Is(err, engine.ErrInvalidTTL) {
				t.Fatalf("PutWithTTL(%s) err = %v, want ErrInvalidTTL", tt.name, err)
			}
			if _, err := eng.Get([]byte(key)); !errors.Is(err, engine.ErrNotFound) {
				t.Errorf("Get after rejected PutWithTTL err = %v, want ErrNotFound (no trace)", err)
			}
		})
	}
}

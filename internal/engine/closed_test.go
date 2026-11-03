package engine_test

import (
	"errors"
	"testing"

	"elena/internal/engine"
)

// Operations on a closed engine must fail with ErrClosed instead of
// touching released file handles.
func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	dir := t.TempDir()

	eng := openEngine(t, dir)
	if err := eng.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := eng.Put([]byte("k2"), []byte("v")); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Put after Close err = %v, want ErrClosed", err)
	}
	if _, err := eng.Get([]byte("k")); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Get after Close err = %v, want ErrClosed", err)
	}
	if err := eng.Delete([]byte("k")); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Delete after Close err = %v, want ErrClosed", err)
	}
}

// Close while a writer is mid-flight must not panic: whichever wins
// the race, the other side observes a clean outcome (successful write
// or ErrClosed), never a crash or a write to a released handle.
func TestCloseRacingWithPuts(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)

	// Seed one acknowledged write before the race so durability has a
	// guaranteed witness; the background writer may or may not get any
	// Put in before Close.
	if err := eng.Put([]byte("seed"), []byte("v")); err != nil {
		t.Fatalf("seed Put failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			key := []byte{byte('a' + i%26)}
			if err := eng.Put(key, []byte("v")); err != nil {
				if !errors.Is(err, engine.ErrClosed) {
					t.Errorf("Put during Close err = %v, want nil or ErrClosed", err)
				}
				return
			}
		}
	}()

	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	<-done

	// Whatever was acknowledged before Close is durable after reopen.
	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)
	if got, err := reopened.Get([]byte("seed")); err != nil || string(got) != "v" {
		t.Errorf("Get(seed) after Close race = %q, %v; want \"v\", nil", got, err)
	}
}

package engine_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/magician336/elena/internal/engine"
)

// Single writer, many readers: while one goroutine keeps writing new
// keys, several readers concurrently look up keys the writer has
// already acknowledged. Acknowledged keys must always read back their
// exact value; the run must be clean under the race detector.
func TestConcurrentSingleWriterManyReaders(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	// Seed keys the readers can rely on for the whole run.
	const seeded = 8
	seededKeys := make([][]byte, seeded)
	for i := range seededKeys {
		seededKeys[i] = []byte(fmt.Sprintf("seed-%d", i))
		if err := eng.Put(seededKeys[i], []byte("stable")); err != nil {
			t.Fatalf("seed Put failed: %v", err)
		}
	}

	const written = 200
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < written; i++ {
			key := []byte(fmt.Sprintf("fresh-%d", i))
			if err := eng.Put(key, []byte(fmt.Sprintf("v-%d", i))); err != nil {
				t.Errorf("concurrent Put(%q) failed: %v", key, err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				// Seeded keys: always present with a stable value.
				key := seededKeys[i%seeded]
				got, err := eng.Get(key)
				if err != nil {
					t.Errorf("reader %d: Get(%q) err = %v, want hit", id, key, err)
					return
				}
				if string(got) != "stable" {
					t.Errorf("reader %d: Get(%q) = %q, want %q", id, key, got, "stable")
					return
				}
				// Fresh keys: either not yet visible or fully visible;
				// a partial state must never leak.
				n := (i + id) % written
				fresh := []byte(fmt.Sprintf("fresh-%d", n))
				got, err = eng.Get(fresh)
				if err == nil {
					if want := fmt.Sprintf("v-%d", n); string(got) != want {
						t.Errorf("reader %d: Get(%q) = %q, want %q or ErrNotFound", id, fresh, got, want)
						return
					}
				} else if !errors.Is(err, engine.ErrNotFound) {
					t.Errorf("reader %d: Get(%q) err = %v, want hit or ErrNotFound", id, fresh, err)
					return
				}
			}
		}(r)
	}

	<-writerDone
	wg.Wait()

	// Every acknowledged write must be present once the dust settles.
	for i := 0; i < written; i++ {
		key := []byte(fmt.Sprintf("fresh-%d", i))
		got, err := eng.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) after writers finished err = %v", key, err)
		}
		if want := fmt.Sprintf("v-%d", i); string(got) != want {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
}

// The engine serializes writers: multiple goroutines issuing Put and
// Delete concurrently must all succeed, and the final state must
// survive a reopen with exactly one live value per key.
func TestConcurrentWritersAreSerialized(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)

	const goroutines = 8
	const perGoroutine = 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				key := []byte(fmt.Sprintf("k-%d-%d", id, i))
				if err := eng.Put(key, []byte("v")); err != nil {
					t.Errorf("Put(%q) failed: %v", key, err)
					return
				}
			}
			// Churn one shared key from every goroutine: last writer
			// wins, no intermediate corruption is observable.
			shared := []byte("shared")
			if err := eng.Put(shared, []byte(fmt.Sprintf("g-%d", id))); err != nil {
				t.Errorf("Put(shared) failed: %v", err)
			}
		}(g)
	}
	wg.Wait()

	if err := eng.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openEngine(t, dir)
	defer closeEngine(t, reopened)
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			key := []byte(fmt.Sprintf("k-%d-%d", g, i))
			if _, err := reopened.Get(key); err != nil {
				t.Fatalf("Get(%q) after reopen err = %v", key, err)
			}
		}
	}
	got, err := reopened.Get([]byte("shared"))
	if err != nil {
		t.Fatalf("Get(shared) err = %v", err)
	}
	if len(got) == 0 {
		t.Error("Get(shared) returned empty value")
	}
}

// Delete racing with Get: the reader sees either the value or
// ErrNotFound, never an error or a torn value. One goroutine
// repeatedly writes and deletes the same key while readers query it.
func TestConcurrentDeleteAndGet(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	key := []byte("flip")
	if err := eng.Put(key, []byte("value")); err != nil {
		t.Fatalf("initial Put failed: %v", err)
	}

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := eng.Put(key, []byte("value")); err != nil {
				t.Errorf("flip Put failed: %v", err)
				return
			}
			if err := eng.Delete(key); err != nil {
				t.Errorf("flip Delete failed: %v", err)
				return
			}
		}
	}()

	var readers sync.WaitGroup
	for r := 0; r < 3; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 200; i++ {
				got, err := eng.Get(key)
				switch {
				case err == nil:
					if string(got) != "value" {
						t.Errorf("Get(flip) = %q, want %q", got, "value")
						return
					}
				case errors.Is(err, engine.ErrNotFound):
					// Deleted state: fine.
				default:
					t.Errorf("Get(flip) err = %v, want hit or ErrNotFound", err)
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	writer.Wait()
}

// Rotation must be invisible to concurrent readers: with a tiny size
// threshold the single writer rotates the active file many times while
// four readers keep looking up keys that live in already-rotated
// immutable files. Seeded keys must always read back exactly; fresh
// keys appear atomically or not at all; the run must be clean under
// the race detector.
func TestConcurrentReadersAcrossRotations(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)

	// Seed keys spread over several already-rotated files.
	seed := writeRotatedFixture(t, eng, 34)

	const churned = 400
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < churned; i++ {
			key := []byte(fmt.Sprintf("churn-%06d", i))
			if err := eng.Put(key, []byte(fmt.Sprintf("cv-%d", i))); err != nil {
				t.Errorf("rotating Put(%q) failed: %v", key, err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				// Seeded keys sit in immutable files: always exact.
				for key, want := range seed {
					got, err := eng.Get([]byte(key))
					if err != nil {
						t.Errorf("reader %d: Get(%q) across rotations err = %v", id, key, err)
						return
					}
					if string(got) != want {
						t.Errorf("reader %d: Get(%q) = %q, want exact fixture value", id, key, got)
						return
					}
				}
				// Churning keys: fully present or not yet visible.
				n := (i + id) % churned
				fresh := []byte(fmt.Sprintf("churn-%06d", n))
				got, err := eng.Get(fresh)
				if err == nil {
					if want := fmt.Sprintf("cv-%d", n); string(got) != want {
						t.Errorf("reader %d: Get(%q) = %q, want %q or ErrNotFound", id, fresh, got, want)
						return
					}
				} else if !errors.Is(err, engine.ErrNotFound) {
					t.Errorf("reader %d: Get(%q) err = %v, want hit or ErrNotFound", id, fresh, err)
					return
				}
			}
		}(r)
	}

	<-writerDone
	wg.Wait()

	// After the dust settles every churned key must be present.
	for i := 0; i < churned; i++ {
		key := []byte(fmt.Sprintf("churn-%06d", i))
		got, err := eng.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) after rotation churn err = %v", key, err)
		}
		if want := fmt.Sprintf("cv-%d", i); string(got) != want {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
}

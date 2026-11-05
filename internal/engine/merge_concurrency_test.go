package engine_test

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/magician336/elena/internal/engine"
)

// Merge must be invisible to concurrent readers and must not stop the
// writer: while one goroutine keeps writing fresh keys (rotating files
// as it goes), another runs Merge back to back, and four readers keep
// serving seeded keys, probing fresh keys, and scanning prefixes.
// Seeded keys must always read exactly; fresh keys appear atomically;
// scans must never fail; the run must be clean under the race
// detector.
func TestConcurrentReadWriteDuringMerge(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, eng)

	// Seed keys spread over several already-rotated immutable files.
	seed := writeRotatedFixture(t, eng, 34)

	const churned = 400
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < churned; i++ {
			key := []byte(fmt.Sprintf("churn-%06d", i))
			if err := eng.Put(key, []byte(fmt.Sprintf("cv-%d", i))); err != nil {
				t.Errorf("concurrent Put(%q) failed: %v", key, err)
				return
			}
		}
	}()

	// Merge back to back while the writer runs. Each merge has real
	// work: the writer keeps producing rotated files and dead churn.
	mergerDone := make(chan struct{})
	go func() {
		defer close(mergerDone)
		for {
			select {
			case <-writerDone:
				return
			default:
			}
			if err := eng.Merge(); err != nil {
				t.Errorf("concurrent Merge() failed: %v", err)
				return
			}
			// Give the writer a chance to make progress instead of
			// merging an unchanged disk in a tight loop.
			for i := 0; i < 20; i++ {
				select {
				case <-writerDone:
					return
				default:
				}
				if err := eng.Put([]byte("merger-noop"), []byte("v")); err != nil {
					t.Errorf("merger Put failed: %v", err)
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				// Seeded keys: exact hits even while merges swap files.
				for key, want := range seed {
					got, err := eng.Get([]byte(key))
					if err != nil {
						t.Errorf("reader %d: Get(%q) during merge err = %v", id, key, err)
						return
					}
					if string(got) != want {
						t.Errorf("reader %d: Get(%q) during merge = %q, want exact fixture value", id, key, got)
						return
					}
				}
				// Fresh keys: fully present or not yet visible.
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
				// Scans snapshot the index and may still be reading
				// through files a concurrent merge is deleting: they
				// must succeed regardless.
				if i%25 == 0 {
					count := 0
					err := eng.Scan(engine.ScanOptions{Prefix: []byte("churn-")}, func(key, value []byte) error {
						count++
						if len(value) == 0 {
							return fmt.Errorf("empty value for %q during merge", key)
						}
						return nil
					})
					if err != nil {
						t.Errorf("reader %d: Scan during merge err = %v", id, err)
						return
					}
				}
			}
		}(r)
	}

	wg.Wait()
	<-mergerDone

	// After the dust settles every churned key must be present exactly.
	for i := 0; i < churned; i++ {
		key := []byte(fmt.Sprintf("churn-%06d", i))
		got, err := eng.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) after merge churn err = %v", key, err)
		}
		if want := fmt.Sprintf("cv-%d", i); string(got) != want {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
	if s := eng.Stats(); s.OpsMerge == 0 {
		t.Error("OpsMerge = 0, want at least one merge to have run during the churn")
	}
}

// Batch commits must keep landing (and surviving) while merges churn
// the file set underneath: a writer commits batches of four keys, a
// merger merges back to back over rotating files, and a reader keeps
// verifying whole batches are either fully invisible or fully visible.
// Afterwards every committed key must be exact and must survive a
// reopen.
func TestConcurrentBatchCommitsDuringMerge(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	const batches = 60
	const perBatch = 4
	var committed atomic.Int64
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for g := 0; g < batches; g++ {
			b := eng.NewBatch()
			for j := 0; j < perBatch; j++ {
				key := []byte(fmt.Sprintf("bgen-%04d-%d", g, j))
				if err := b.Put(key, []byte(fmt.Sprintf("v-%d-%d", g, j))); err != nil {
					t.Errorf("batch Put(%q) during merge failed: %v", key, err)
					return
				}
			}
			if err := b.Commit(); err != nil {
				t.Errorf("Commit during merge failed: %v", err)
				return
			}
			committed.Store(int64(g + 1)) // generations [0, g] are committed
		}
	}()

	mergerDone := make(chan struct{})
	go func() {
		defer close(mergerDone)
		for {
			select {
			case <-writerDone:
				return
			default:
			}
			if err := eng.Merge(); err != nil {
				t.Errorf("Merge during batch commits failed: %v", err)
				return
			}
			for i := 0; i < 10; i++ {
				select {
				case <-writerDone:
					return
				default:
				}
				if err := eng.Put([]byte("merger-noop"), []byte("v")); err != nil {
					t.Errorf("merger Put failed: %v", err)
					return
				}
			}
		}
	}()

	var readers sync.WaitGroup
	for r := 0; r < 2; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 200; i++ {
				// One snapshot (Scan) is one moment in time; committed
				// generations must be whole in it, in-flight ones whole
				// or absent. Sequential Gets could not prove this — a
				// commit may legally land between two Gets.
				base := int(committed.Load())
				visible := make(map[int]int)
				err := eng.Scan(engine.ScanOptions{Prefix: []byte("bgen-")}, func(key, _ []byte) error {
					g, err := strconv.Atoi(string(key[5:9]))
					if err != nil {
						return fmt.Errorf("unexpected scan key %q: %w", key, err)
					}
					visible[g]++
					return nil
				})
				if err != nil {
					t.Errorf("reader Scan during merge err = %v", err)
					return
				}
				for g, n := range visible {
					if n != perBatch {
						t.Errorf("batch generation %d half-visible during merge: %d of %d", g, n, perBatch)
						return
					}
				}
				for g := 0; g < base && g < batches; g++ {
					if visible[g] != perBatch {
						t.Errorf("committed batch generation %d incomplete during merge (base=%d): %d of %d", g, base, visible[g], perBatch)
						return
					}
				}
			}
		}()
	}
	readers.Wait()
	<-mergerDone
	<-writerDone

	if err := eng.Close(); err != nil {
		t.Fatalf("Close after concurrent batch+merge failed: %v", err)
	}
	reopened := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, reopened)
	for g := 0; g < batches; g++ {
		for j := 0; j < perBatch; j++ {
			key := fmt.Sprintf("bgen-%04d-%d", g, j)
			got, err := reopened.Get([]byte(key))
			if err != nil || string(got) != fmt.Sprintf("v-%d-%d", g, j) {
				t.Fatalf("Get(%q) after reopen = %q, %v; want exact committed value", key, got, err)
			}
		}
	}
}

// countOpenFDs returns how many file descriptors this process holds.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count open fds on this platform: %v", err)
	}
	return len(entries)
}

// A merge retires the handles of the files it reclaims. Those handles
// must not pile up across many merges: once no scan snapshot reads
// through them anymore they close, so repeated merges leave the
// descriptor count flat.
func TestMergeRetiredHandlesDoNotLeak(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, engine.Options{})
	defer closeEngine(t, eng)

	mustPut(t, eng, "stable", "v")
	if err := eng.Merge(); err != nil {
		t.Fatalf("initial Merge failed: %v", err)
	}
	baseline := countOpenFDs(t)

	for round := 0; round < 60; round++ {
		for i := 0; i < 4; i++ {
			mustPut(t, eng, fmt.Sprintf("k-%d-%d", round, i), "v")
		}
		if err := eng.Merge(); err != nil {
			t.Fatalf("Merge round %d failed: %v", round, err)
		}
	}

	after := countOpenFDs(t)
	if after > baseline+4 {
		t.Errorf("open fds after 60 merges = %d, baseline %d: retired handles leaked", after, baseline)
	}
}

// Close racing a running merge must leave the directory consistent:
// the merge either finished (its output and old files are in order)
// or was aborted (ErrClosed, its half-written output discarded or
// harmlessly replayable). Either way a reopen serves every
// acknowledged write and the engine releases the directory lock.
func TestMergeRacingCloseKeepsDirectoryReopenable(t *testing.T) {
	dir := t.TempDir()
	eng := openEngineWithOptions(t, dir, smallFileOptions())

	// Enough data that a merge is still rewriting when Close strikes.
	want := writeRotatedFixture(t, eng, 3000)

	mergeDone := make(chan error, 1)
	go func() { mergeDone <- eng.Merge() }()
	// Land somewhere inside the rewrite phase; exact timing does not
	// matter — both outcomes are legal and must be safe.
	time.Sleep(2 * time.Millisecond)
	if err := eng.Close(); err != nil {
		t.Errorf("Close racing a merge failed: %v", err)
	}
	mergeErr := <-mergeDone
	if mergeErr != nil && !errors.Is(mergeErr, engine.ErrClosed) {
		t.Errorf("Merge racing Close err = %v, want nil or ErrClosed", mergeErr)
	}

	reopened := openEngineWithOptions(t, dir, smallFileOptions())
	defer closeEngine(t, reopened)
	assertAllReadable(t, reopened, want)
}

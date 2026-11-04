package elena_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Appends from many goroutines must hand out every sequence number
// from 1 to N exactly once — no holes, no duplicates, no lost events —
// and the full read must agree, event for event, with what the
// appenders were told.
func TestConcurrentAppendIsHoleFree(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "many")

	const goroutines = 8
	const perGoroutine = 25
	total := goroutines * perGoroutine

	type appended struct {
		seq     uint64
		payload string
	}
	results := make(chan appended, total)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				payload := fmt.Sprintf("g%d-%d", g, i)
				seq, err := sess.Append([]byte(payload))
				if err != nil {
					t.Errorf("Append() error: %v", err)
					return
				}
				results <- appended{seq: seq, payload: payload}
			}
		}(g)
	}
	wg.Wait()
	close(results)

	seen := make(map[uint64]string, total)
	for res := range results {
		if dup, ok := seen[res.seq]; ok {
			t.Fatalf("sequence %d handed out twice (%q and %q)", res.seq, dup, res.payload)
		}
		seen[res.seq] = res.payload
	}
	if len(seen) != total {
		t.Fatalf("%d distinct sequence numbers handed out, want %d", len(seen), total)
	}
	for seq := uint64(1); seq <= uint64(total); seq++ {
		if _, ok := seen[seq]; !ok {
			t.Fatalf("sequence %d missing: the sequence has a hole", seq)
		}
	}

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("Events() failed: %v", err)
	}
	if len(events) != total {
		t.Fatalf("Events() returned %d events, want %d", len(events), total)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("Events() position %d carries Seq = %d, want %d", i, ev.Seq, i+1)
		}
		if got := seen[ev.Seq]; got != string(ev.Payload) {
			t.Fatalf("event %d payload = %q, want the appended %q", ev.Seq, ev.Payload, got)
		}
	}
}

// Two goroutines creating the same session concurrently must end up
// with one shared trajectory: their combined appends are hole-free.
func TestConcurrentCreateSessionSharesTrajectory(t *testing.T) {
	st := openStore(t)

	const goroutines = 4
	const perGoroutine = 20
	total := goroutines * perGoroutine

	results := make(chan uint64, total)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := st.CreateSession("runs", "shared")
			if err != nil {
				t.Errorf("CreateSession() error: %v", err)
				return
			}
			for i := 0; i < perGoroutine; i++ {
				seq, err := sess.Append([]byte("event"))
				if err != nil {
					t.Errorf("Append() error: %v", err)
					return
				}
				results <- seq
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[uint64]bool, total)
	for seq := range results {
		if seen[seq] {
			t.Fatalf("sequence %d handed out twice", seq)
		}
		seen[seq] = true
	}
	if len(seen) != total {
		t.Fatalf("%d distinct sequence numbers handed out, want %d", len(seen), total)
	}
}

// Reads must run concurrently with appends: every full read observes a
// consistent prefix of the trajectory — sequence numbers 1..k in order
// — and once the writers finish, the final read sees every event.
func TestConcurrentReadsDuringAppend(t *testing.T) {
	st := openStore(t)
	sess := createSession(t, st, "runs", "busy")

	const writers = 4
	const perWriter = 25
	total := writers * perWriter

	writing := atomic.Bool{}
	writing.Store(true)

	var writerGroup sync.WaitGroup
	for w := 0; w < writers; w++ {
		writerGroup.Add(1)
		go func(w int) {
			defer writerGroup.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := sess.Append([]byte(fmt.Sprintf("w%d-%d", w, i))); err != nil {
					t.Errorf("Append() error: %v", err)
					return
				}
			}
		}(w)
	}

	readsDone := make(chan struct{})
	go func() {
		defer close(readsDone)
		for writing.Load() {
			events, err := sess.Events()
			if err != nil {
				t.Errorf("Events() error: %v", err)
				return
			}
			for i, ev := range events {
				if ev.Seq != uint64(i+1) {
					t.Errorf("read observed sequence %d at position %d: not a consistent prefix", ev.Seq, i)
					return
				}
			}
			if _, err := sess.Event(1); err != nil && len(events) > 0 {
				t.Errorf("Event(1) error while trajectory holds events: %v", err)
				return
			}
		}
	}()

	writerGroup.Wait()
	writing.Store(false)
	<-readsDone

	events, err := sess.Events()
	if err != nil {
		t.Fatalf("final Events() failed: %v", err)
	}
	if len(events) != total {
		t.Fatalf("final Events() returned %d events, want %d", len(events), total)
	}
}

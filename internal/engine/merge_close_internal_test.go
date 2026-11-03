package engine

import (
	"errors"
	"fmt"
	"testing"
)

// A merge whose index switch finds the engine closed must keep the
// successor active file: writes acknowledged between the freeze and
// the Close landed there — durable and synced under the default always
// policy — so deleting that file would break "ack means durable".
// Only the merge output, which no reader ever saw, is garbage. The
// phases are driven directly so Close lands exactly between the
// rewrite and the switch; a timing-only race almost always misses that
// window from the other side (the rewrite bails with ErrClosed once
// Close has shut its handles).
func TestMergeClosedAtSwitchKeepsPostFreezeAcknowledgedWrites(t *testing.T) {
	dir := t.TempDir()
	opts := Options{MaxDataFileSize: 4 * (28 + 7 + 20)}
	eng, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}

	// A fixture large enough to span several rotated files, so the
	// frozen set is real and the rewrite has actual work.
	const fixture = 24
	want := map[string]string{}
	for i := 0; i < fixture; i++ {
		key := fmt.Sprintf("key%03d", i)
		value := fmt.Sprintf("value%03d", i)
		if err := eng.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}

	// Drive the merge as Merge() would, one phase at a time.
	eng.mergeMu.Lock()
	defer eng.mergeMu.Unlock()

	frozen, ok, err := eng.freezeMergeSet()
	if err != nil {
		t.Fatalf("freezeMergeSet failed: %v", err)
	}
	if !ok {
		t.Fatal("freezeMergeSet reported nothing to merge with a live fixture")
	}

	// The write the bug loses: acknowledged after the freeze (it lands
	// in the successor active file), before Close, durable and indexed.
	const postFreezeKey = "post-freeze-acked"
	const postFreezeValue = "durable"
	if err := eng.Put([]byte(postFreezeKey), []byte(postFreezeValue)); err != nil {
		t.Fatalf("Put after freeze failed: %v", err)
	}
	want[postFreezeKey] = postFreezeValue

	newRefs, flushed, err := eng.rewriteInto(frozen.out, frozen.outID, frozen.merged)
	if err != nil {
		t.Fatalf("rewriteInto failed: %v", err)
	}

	// Close completes while the merge sits between its rewrite and its
	// index switch — the exact interleaving whose files the switch must
	// choose between.
	if err := eng.Close(); err != nil {
		t.Fatalf("Close between merge phases failed: %v", err)
	}

	switchErr := eng.switchToMerged(frozen, newRefs, flushed)
	if !errors.Is(switchErr, ErrClosed) {
		t.Fatalf("switchToMerged after Close err = %v, want ErrClosed", switchErr)
	}

	reopened, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("reopen after merge aborted by Close failed: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close after reopen failed: %v", err)
		}
	}()
	for key, value := range want {
		got, err := reopened.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q) after reopen err = %v; the acknowledged write was lost", key, err)
		}
		if string(got) != value {
			t.Errorf("Get(%q) after reopen = %q, want the exact fixture value", key, got)
		}
	}
}

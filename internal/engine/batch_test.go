package engine_test

import (
	"fmt"
	"strings"
	"testing"
)

// Batches provide durable atomicity, not transactions: one Commit =
// one append buffer + one fdatasync + one index publication, so a
// crash leaves the whole batch durable or the whole batch invisible
// (a torn tail is truncated at the next Open). There is no isolation
// and no rollback — see the Batch doc comment. These tests pin that
// contract through the public API.

// A batch accumulates many Puts; a single Commit publishes them all,
// and every key is immediately readable with its exact value.
func TestBatchCommitPublishesAllWrites(t *testing.T) {
	dir := t.TempDir()
	eng := openEngine(t, dir)
	defer closeEngine(t, eng)

	b := eng.NewBatch()
	want := map[string]string{}
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("batch-key-%03d", i)
		value := fmt.Sprintf("value-%03d-%s", i, strings.Repeat("x", i))
		if err := b.Put([]byte(key), []byte(value)); err != nil {
			t.Fatalf("batch Put(%q) failed: %v", key, err)
		}
		want[key] = value
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	assertAllReadable(t, eng, want)
}

package engine_test

import (
	"testing"

	"github.com/magician336/elena/internal/engine"
)

// Overwriting a key must make the newest value win immediately: the
// index points at the newest entry and Get never observes the older
// value afterwards. The old entry stays on disk (dead bytes until a
// later merge) but is invisible to readers.
func TestOverwriteLastWriteWins(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	key := []byte("overwritten")
	for _, value := range []string{"v1", "v2", "v3"} {
		if err := eng.Put(key, []byte(value)); err != nil {
			t.Fatalf("Put(%q, %q) failed: %v", key, value, err)
		}
	}

	got, err := eng.Get(key)
	if err != nil {
		t.Fatalf("Get(%q) failed: %v", key, err)
	}
	if string(got) != "v3" {
		t.Errorf("Get(%q) = %q, want %q (last write must win)", key, got, "v3")
	}
}

// Overwrite interaction with Delete: a Delete followed by a Put of the
// same key must make the key readable again with the new value; a Put
// followed by Delete must keep it hidden.
func TestOverwriteAfterDeleteSequence(t *testing.T) {
	dir := t.TempDir()

	eng, err := engine.Open(dir, engine.Options{})
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	defer closeEngine(t, eng)

	key := []byte("seq")
	steps := []struct {
		op     string
		value  string
		wantOK bool
	}{
		{op: "put", value: "first", wantOK: true},
		{op: "delete", wantOK: false},
		{op: "put", value: "second", wantOK: true},
		{op: "delete", wantOK: false},
	}
	for i, step := range steps {
		switch step.op {
		case "put":
			if err := eng.Put(key, []byte(step.value)); err != nil {
				t.Fatalf("step %d Put failed: %v", i, err)
			}
		case "delete":
			if err := eng.Delete(key); err != nil {
				t.Fatalf("step %d Delete failed: %v", i, err)
			}
		}
		got, err := eng.Get(key)
		if step.wantOK {
			if err != nil {
				t.Fatalf("step %d Get err = %v, want value %q", i, err, step.value)
			}
			if string(got) != step.value {
				t.Errorf("step %d Get = %q, want %q", i, got, step.value)
			}
		} else if err != engine.ErrNotFound {
			t.Fatalf("step %d Get err = %v, want ErrNotFound", i, err)
		}
	}
}

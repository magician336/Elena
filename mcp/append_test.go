package mcp

import "testing"

func TestAppendEventTool(t *testing.T) {
	s := newSession(t)

	// Appending to an absent session creates it as a perpetual one and
	// hands out sequence numbers from 1.
	var first struct {
		Seq uint64 `json:"seq"`
	}
	s.call("append_event", map[string]any{
		"namespace": "runs",
		"session":   "alpha",
		"payload":   "event one",
	}, &first)
	if first.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", first.Seq)
	}

	// The second append continues the same trajectory.
	var second struct {
		Seq uint64 `json:"seq"`
	}
	s.call("append_event", map[string]any{
		"namespace": "runs",
		"session":   "alpha",
		"payload":   "event two",
	}, &second)
	if second.Seq != 2 {
		t.Fatalf("second seq = %d, want 2", second.Seq)
	}

	// A timed session created through the tool accepts appends too.
	var timed struct {
		Seq uint64 `json:"seq"`
	}
	s.call("create_session", map[string]any{
		"namespace":   "runs",
		"session":     "beta",
		"ttl_seconds": 3600,
	}, new(any))
	s.call("append_event", map[string]any{
		"namespace": "runs",
		"session":   "beta",
		"payload":   "timed event",
	}, &timed)
	if timed.Seq != 1 {
		t.Fatalf("timed session first seq = %d, want 1", timed.Seq)
	}
}

func TestAppendEventToolInvalidPayload(t *testing.T) {
	s := newSession(t)
	// The payload argument is required by the input schema; omitting it
	// is rejected before the handler runs.
	if got := s.callError("append_event", map[string]any{
		"namespace": "runs",
		"session":   "alpha",
	}); !contains(got, "payload") {
		t.Fatalf("error %q does not mention %q", got, "payload")
	}
}

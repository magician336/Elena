package mcp

import "testing"

func TestStatsTool(t *testing.T) {
	s := newSession(t)
	s.call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "event one",
	}, new(any))

	var out struct {
		LiveEntries uint64  `json:"live_entries"`
		DataBytes   uint64  `json:"data_bytes"`
		DeadRatio   float64 `json:"dead_ratio"`
	}
	s.call("stats", map[string]any{}, &out)
	if out.LiveEntries == 0 {
		t.Fatal("live entries is zero after an append")
	}
	if out.DataBytes == 0 {
		t.Fatal("data bytes is zero after an append")
	}
}

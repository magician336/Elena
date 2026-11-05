package mcp

import "testing"

// readOut mirrors read_trajectory's output for assertions.
type readOut struct {
	Events []struct {
		Seq        uint64 `json:"seq"`
		AppendedAt string `json:"appended_at"`
		Payload    string `json:"payload"`
	} `json:"events"`
}

func TestReadTrajectoryTool(t *testing.T) {
	s := newSession(t)
	for _, payload := range []string{"event one", "event two", "event three"} {
		s.call("append_event", map[string]any{
			"namespace": "runs",
			"session":   "alpha",
			"payload":   payload,
		}, new(any))
	}

	tests := []struct {
		name     string
		args     map[string]any
		wantSeqs []uint64
	}{
		{
			name:     "full trajectory",
			args:     map[string]any{"namespace": "runs", "session": "alpha"},
			wantSeqs: []uint64{1, 2, 3},
		},
		{
			name:     "point-in-time read at seq 2",
			args:     map[string]any{"namespace": "runs", "session": "alpha", "upto_seq": 2},
			wantSeqs: []uint64{1, 2},
		},
		{
			name:     "point-in-time read below the first event",
			args:     map[string]any{"namespace": "runs", "session": "alpha", "upto_seq": 0},
			wantSeqs: nil,
		},
		{
			name:     "point-in-time read beyond the stored tail",
			args:     map[string]any{"namespace": "runs", "session": "alpha", "upto_seq": 99},
			wantSeqs: []uint64{1, 2, 3},
		},
		{
			name:     "absent session reads empty, and stays absent",
			args:     map[string]any{"namespace": "runs", "session": "absent"},
			wantSeqs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out readOut
			s.call("read_trajectory", tt.args, &out)
			if got := seqs(out); !equalSeqs(got, tt.wantSeqs) {
				t.Fatalf("seqs = %v, want %v", got, tt.wantSeqs)
			}
		})
	}

	// The read of an absent session must not create it: the namespace
	// listing stays empty of the absent name.
	infos, err := s.server.store.Sessions("runs")
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	for _, info := range infos {
		if info.ID == "absent" {
			t.Fatal("reading an absent session created it")
		}
	}
}

func TestReadTrajectoryToolPayloads(t *testing.T) {
	s := newSession(t)
	s.call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "hello",
	}, new(any))
	var out readOut
	s.call("read_trajectory", map[string]any{"namespace": "runs", "session": "alpha"}, &out)
	if len(out.Events) != 1 || out.Events[0].Payload != "hello" {
		t.Fatalf("payloads = %+v, want one event with the appended bytes", out.Events)
	}
	if out.Events[0].AppendedAt == "" {
		t.Fatal("append timestamp missing from the read")
	}
}

func seqs(out readOut) []uint64 {
	var got []uint64
	for _, ev := range out.Events {
		got = append(got, ev.Seq)
	}
	return got
}

func equalSeqs(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

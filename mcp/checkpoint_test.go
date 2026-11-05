package mcp

import "testing"

func TestCheckpointTools(t *testing.T) {
	s := newSession(t)
	for _, payload := range []string{"event one", "event two"} {
		s.call("append_event", map[string]any{
			"namespace": "runs", "session": "alpha", "payload": payload,
		}, new(any))
	}

	// create_checkpoint records the whole-store baseline.
	var created struct {
		CheckpointID uint64 `json:"checkpoint_id"`
	}
	s.call("create_checkpoint", map[string]any{}, &created)
	if created.CheckpointID == 0 {
		t.Fatal("checkpoint ID is zero")
	}

	// Events past the baseline stay readable; the restore resets the
	// counters to the recorded baseline.
	var appended struct {
		Seq uint64 `json:"seq"`
	}
	s.call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "event three",
	}, &appended)
	if appended.Seq != 3 {
		t.Fatalf("seq after baseline = %d, want 3", appended.Seq)
	}

	var restored struct {
		CheckpointID uint64 `json:"checkpoint_id"`
		Trajectories []struct {
			Namespace  string `json:"namespace"`
			Session    string `json:"session"`
			ReachedSeq uint64 `json:"reached_seq"`
		} `json:"trajectories"`
	}
	s.call("restore_checkpoint", map[string]any{"checkpoint_id": created.CheckpointID}, &restored)
	if restored.CheckpointID != created.CheckpointID {
		t.Fatalf("restored ID = %d, want %d", restored.CheckpointID, created.CheckpointID)
	}
	if len(restored.Trajectories) != 1 ||
		restored.Trajectories[0].Namespace != "runs" ||
		restored.Trajectories[0].Session != "alpha" ||
		restored.Trajectories[0].ReachedSeq != 2 {
		t.Fatalf("restored manifest = %+v, want one runs/alpha baseline at seq 2", restored.Trajectories)
	}

	// After the restore, the next append continues from the baseline's
	// sequence number, overwriting the stored seq 3.
	var after struct {
		Seq uint64 `json:"seq"`
	}
	s.call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "event three again",
	}, &after)
	if after.Seq != 3 {
		t.Fatalf("seq after restore = %d, want 3 (the baseline's next)", after.Seq)
	}
	var out readOut
	s.call("read_trajectory", map[string]any{"namespace": "runs", "session": "alpha"}, &out)
	if len(out.Events) != 3 || out.Events[2].Payload != "event three again" {
		t.Fatalf("trajectory after restore = %+v, want the overwritten tail last", out.Events)
	}
}

func TestRestoreCheckpointUnknownID(t *testing.T) {
	s := newSession(t)
	got := s.callError("restore_checkpoint", map[string]any{"checkpoint_id": 99})
	if !contains(got, "checkpoint") {
		t.Fatalf("error %q does not mention %q", got, "checkpoint")
	}
}

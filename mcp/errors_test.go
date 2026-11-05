package mcp

import (
	"context"
	"testing"

	"github.com/magician336/elena"
)

func TestNewRejectsNilOpener(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) succeeded, want an error")
	}
}

func TestNewOpenFailure(t *testing.T) {
	_, err := New(func() (*elena.Store, error) { return nil, elena.ErrDirectoryLocked })
	if err == nil {
		t.Fatal("opener failure was not reported")
	}
}

// TestHandlersAfterClose drives every handler against a closed store:
// each must surface the failure, not swallow it — the same discipline
// the library's own facade follows.
func TestHandlersAfterClose(t *testing.T) {
	srv, err := New(func() (*elena.Store, error) { return elena.Open(t.TempDir()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()

	if _, _, err := srv.createSession(ctx, nil, createSessionIn{
		Namespace: "runs", Session: "alpha",
	}); err == nil {
		t.Error("createSession after Close: no error")
	}
	if _, _, err := srv.appendEvent(ctx, nil, appendEventIn{
		Namespace: "runs", Session: "alpha", Payload: "x",
	}); err == nil {
		t.Error("appendEvent after Close: no error")
	}
	if _, _, err := srv.readTrajectory(ctx, nil, readTrajectoryIn{
		Namespace: "runs", Session: "alpha",
	}); err == nil {
		t.Error("readTrajectory after Close: no error")
	}
	if _, _, err := srv.createCheckpoint(ctx, nil, createCheckpointIn{}); err == nil {
		t.Error("createCheckpoint after Close: no error")
	}
	if _, _, err := srv.restoreCheckpoint(ctx, nil, restoreCheckpointIn{
		CheckpointID: 1,
	}); err == nil {
		t.Error("restoreCheckpoint after Close: no error")
	}
}

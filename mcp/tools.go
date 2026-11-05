package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/magician336/elena"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools declares the whole tool set: six tools over the elena
// public API, their input schemas derived from the argument structs by
// the SDK's schema reflection.
func (s *Server) registerTools() {
	mcpAddTool(s.sdk, &sdk.Tool{
		Name:        "create_session",
		Description: "Create a session in a namespace, owning one append-only trajectory. Perpetual by default, or timed with a TTL in seconds whose single deadline covers the session's metadata and every event of its trajectory; once the deadline elapses the whole session reads as if never written. Creating an existing session of the same lifecycle form is idempotent; across forms it fails.",
	}, s.createSession)
	mcpAddTool(s.sdk, &sdk.Tool{
		Name:        "append_event",
		Description: "Append one event to a session's trajectory and return the event's sequence number, starting at 1 and increasing by one per event. An absent session is created as a perpetual one; a timed session keeps its stored deadline. The payload is stored unmodified as the event's bytes.",
	}, s.appendEvent)
	mcpAddTool(s.sdk, &sdk.Tool{
		Name:        "read_trajectory",
		Description: "Read a session's trajectory in sequence order: every event with its sequence number, append timestamp, and payload. With upto_seq the read is a point-in-time read — every event whose sequence number is at most upto_seq, endpoint included; without it the whole stored trajectory. An absent session reads empty; a timed session whose deadline has elapsed reads empty too, as if never written.",
	}, s.readTrajectory)
	mcpAddTool(s.sdk, &sdk.Tool{
		Name:        "create_checkpoint",
		Description: "Record one whole-store checkpoint: every live trajectory's reached sequence number and every session's deadline, written as one durable record. Returns the checkpoint's ID, which restore_checkpoint takes back.",
	}, s.createCheckpoint)
	mcpAddTool(s.sdk, &sdk.Tool{
		Name:        "restore_checkpoint",
		Description: "Restore one checkpoint by ID: the runtime view — every trajectory's sequence counter and the session table — resets to the recorded baseline, and appends afterwards continue from the baseline's sequence numbers. Events appended past the baseline stay on disk and stay visible to full reads; the restore touches no data. An unknown ID changes nothing and fails.",
	}, s.restoreCheckpoint)
}

// createCheckpointIn is the create_checkpoint argument schema; the
// checkpoint covers the whole store, so the call takes no arguments.
type createCheckpointIn struct{}

// createCheckpointOut is the create_checkpoint output schema.
type createCheckpointOut struct {
	CheckpointID uint64 `json:"checkpoint_id" jsonschema:"the ID of the recorded checkpoint"`
}

// createCheckpoint records the baseline through the public API.
func (s *Server) createCheckpoint(_ context.Context, _ *sdk.CallToolRequest, _ createCheckpointIn) (*sdk.CallToolResult, createCheckpointOut, error) {
	id, err := s.store.CreateCheckpoint()
	if err != nil {
		return nil, createCheckpointOut{}, fmt.Errorf("creating checkpoint: %w", err)
	}
	return nil, createCheckpointOut{CheckpointID: id}, nil
}

// restoreCheckpointIn is the restore_checkpoint argument schema.
type restoreCheckpointIn struct {
	CheckpointID uint64 `json:"checkpoint_id" jsonschema:"the ID of the checkpoint to restore"`
}

// restoredBaselineOut is one trajectory's line of a restored baseline.
type restoredBaselineOut struct {
	Namespace  string `json:"namespace" jsonschema:"the trajectory's namespace"`
	Session    string `json:"session" jsonschema:"the session ID owning the trajectory"`
	ReachedSeq uint64 `json:"reached_seq" jsonschema:"the sequence number the baseline recorded; a point-in-time read up to it sees exactly what the checkpoint saw"`
}

// restoreCheckpointOut is the restore_checkpoint output schema.
type restoreCheckpointOut struct {
	CheckpointID uint64                `json:"checkpoint_id" jsonschema:"the restored checkpoint's ID"`
	Trajectories []restoredBaselineOut `json:"trajectories" jsonschema:"the baseline the restore reset to, one line per recorded trajectory"`
}

// restoreCheckpoint resets the runtime view to the recorded baseline
// and echoes the manifest it restored, so the caller sees which
// sequence numbers the next appends continue from.
func (s *Server) restoreCheckpoint(_ context.Context, _ *sdk.CallToolRequest, in restoreCheckpointIn) (*sdk.CallToolResult, restoreCheckpointOut, error) {
	if err := s.store.Restore(in.CheckpointID); err != nil {
		return nil, restoreCheckpointOut{}, fmt.Errorf("restoring checkpoint %d: %w", in.CheckpointID, err)
	}
	manifest, err := s.store.Checkpoint(in.CheckpointID)
	if err != nil {
		return nil, restoreCheckpointOut{}, fmt.Errorf("reading checkpoint %d after restore: %w", in.CheckpointID, err)
	}
	out := restoreCheckpointOut{
		CheckpointID: manifest.ID,
		Trajectories: make([]restoredBaselineOut, 0, len(manifest.Trajectories)),
	}
	for _, baseline := range manifest.Trajectories {
		out.Trajectories = append(out.Trajectories, restoredBaselineOut{
			Namespace:  baseline.Namespace,
			Session:    baseline.Session,
			ReachedSeq: baseline.ReachedSeq,
		})
	}
	return nil, out, nil
}

// readTrajectoryIn is the read_trajectory argument schema.
type readTrajectoryIn struct {
	Namespace string  `json:"namespace" jsonschema:"the session's namespace"`
	Session   string  `json:"session" jsonschema:"the session's ID within its namespace"`
	UptoSeq   *uint64 `json:"upto_seq,omitempty" jsonschema:"optional sequence ceiling for a point-in-time read; absent reads the whole trajectory"`
}

// trajectoryEventOut is one event of read_trajectory's output.
type trajectoryEventOut struct {
	Seq        uint64 `json:"seq" jsonschema:"the event's sequence number"`
	AppendedAt string `json:"appended_at" jsonschema:"RFC3339 time the event was appended"`
	Payload    string `json:"payload" jsonschema:"the event's stored bytes"`
}

// readTrajectoryOut is the read_trajectory output schema.
type readTrajectoryOut struct {
	Events []trajectoryEventOut `json:"events" jsonschema:"the trajectory's events in sequence order"`
}

// readTrajectory reads through the public API, one instant per call.
func (s *Server) readTrajectory(_ context.Context, _ *sdk.CallToolRequest, in readTrajectoryIn) (*sdk.CallToolResult, readTrajectoryOut, error) {
	sess, err := s.readHandle(in.Namespace, in.Session)
	if errors.Is(err, errSessionAbsent) {
		return nil, readTrajectoryOut{Events: []trajectoryEventOut{}}, nil
	}
	if err != nil {
		return nil, readTrajectoryOut{}, err
	}
	var events []elena.Event
	if in.UptoSeq != nil {
		events, err = sess.EventsUpTo(*in.UptoSeq)
	} else {
		events, err = sess.Events()
	}
	if err != nil {
		return nil, readTrajectoryOut{}, fmt.Errorf("reading trajectory %s/%s: %w", in.Namespace, in.Session, err)
	}
	out := readTrajectoryOut{Events: make([]trajectoryEventOut, 0, len(events))}
	for _, ev := range events {
		out.Events = append(out.Events, trajectoryEventOut{
			Seq:        ev.Seq,
			AppendedAt: ev.AppendedAt.Format(time.RFC3339),
			Payload:    string(ev.Payload),
		})
	}
	return nil, out, nil
}

// appendEventIn is the append_event argument schema.
type appendEventIn struct {
	Namespace string `json:"namespace" jsonschema:"the session's namespace"`
	Session   string `json:"session" jsonschema:"the session's ID within its namespace"`
	Payload   string `json:"payload" jsonschema:"the event's payload as UTF-8 text, stored unmodified"`
}

// appendEventOut is the append_event output schema.
type appendEventOut struct {
	Seq uint64 `json:"seq" jsonschema:"the sequence number assigned to the appended event"`
}

// appendEvent appends one event through the public API.
func (s *Server) appendEvent(_ context.Context, _ *sdk.CallToolRequest, in appendEventIn) (*sdk.CallToolResult, appendEventOut, error) {
	sess, err := s.writeHandle(in.Namespace, in.Session)
	if err != nil {
		return nil, appendEventOut{}, err
	}
	seq, err := sess.Append([]byte(in.Payload))
	if err != nil {
		return nil, appendEventOut{}, fmt.Errorf("appending to %s/%s: %w", in.Namespace, in.Session, err)
	}
	return nil, appendEventOut{Seq: seq}, nil
}

// createSessionIn is the create_session argument schema.
type createSessionIn struct {
	Namespace  string  `json:"namespace" jsonschema:"the session's namespace"`
	Session    string  `json:"session" jsonschema:"the session's ID within its namespace"`
	TTLSeconds *uint64 `json:"ttl_seconds,omitempty" jsonschema:"optional time-to-live in seconds; absent creates a perpetual session"`
}

// createSessionOut is the create_session output schema.
type createSessionOut struct {
	Namespace string  `json:"namespace" jsonschema:"the session's namespace"`
	Session   string  `json:"session" jsonschema:"the session's ID"`
	Deadline  *string `json:"deadline,omitempty" jsonschema:"RFC3339 deadline of a timed session; absent for a perpetual one"`
}

// createSession creates one session and reads back the deadline the
// store recorded for it — the stored deadline governs, not the caller's
// clock.
func (s *Server) createSession(_ context.Context, _ *sdk.CallToolRequest, in createSessionIn) (*sdk.CallToolResult, createSessionOut, error) {
	var err error
	if in.TTLSeconds != nil {
		ttl := time.Duration(*in.TTLSeconds) * time.Second
		_, err = s.store.CreateSessionWithTTL(in.Namespace, in.Session, ttl)
	} else {
		_, err = s.store.CreateSession(in.Namespace, in.Session)
	}
	if err != nil {
		return nil, createSessionOut{}, fmt.Errorf("creating session %s/%s: %w", in.Namespace, in.Session, err)
	}
	deadline, err := s.storedDeadline(in.Namespace, in.Session)
	if err != nil {
		return nil, createSessionOut{}, err
	}
	out := createSessionOut{Namespace: in.Namespace, Session: in.Session}
	if deadline != nil {
		formatted := deadline.Format(time.RFC3339)
		out.Deadline = &formatted
	}
	return nil, out, nil
}

// storedDeadline reads a session's recorded deadline back from the
// namespace listing: nil marks a perpetual session. The listing is the
// library's own read path, so the answer follows the stored entries.
func (s *Server) storedDeadline(namespace, session string) (*time.Time, error) {
	infos, err := s.store.Sessions(namespace)
	if err != nil {
		return nil, fmt.Errorf("listing sessions of namespace %s: %w", namespace, err)
	}
	for _, info := range infos {
		if info.ID != session {
			continue
		}
		if info.Deadline.IsZero() {
			return nil, nil
		}
		d := info.Deadline
		return &d, nil
	}
	return nil, fmt.Errorf("session %s/%s absent from the namespace listing", namespace, session)
}

// mcpAddTool wraps the SDK's generic AddTool so each handler above
// reads as a plain function of its arguments.
func mcpAddTool[In, Out any](server *sdk.Server, tool *sdk.Tool, h sdk.ToolHandlerFor[In, Out]) {
	sdk.AddTool(server, tool, h)
}

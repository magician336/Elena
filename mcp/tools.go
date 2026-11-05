package mcp

import (
	"context"
	"fmt"
	"time"

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

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/magician336/elena"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// session is the client-side seam every tool test drives: the server is
// built over a real elena store in a temp directory, connected through
// the SDK's in-memory transport, and exercised with CallTool — the same
// request path a runtime client takes, minus the byte transport.
type session struct {
	t       *testing.T
	client  *sdk.ClientSession
	server  *Server
	cleanup func()
}

// newSession starts one server over a fresh store and connects a client
// to it.
func newSession(t *testing.T) *session {
	t.Helper()
	dir := t.TempDir()
	return newSessionAt(t, dir)
}

// newSessionAt starts one server over the store at dir and connects a
// client to it; tests that reopen the same directory use it.
func newSessionAt(t *testing.T, dir string) *session {
	t.Helper()
	srv, err := New(func() (*elena.Store, error) { return elena.Open(dir) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clientTrans, serverTrans := sdk.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := srv.sdk.Connect(ctx, serverTrans, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTrans, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	s := &session{t: t, client: clientSession, server: srv}
	s.cleanup = func() {
		_ = clientSession.Close()
		serverSession.Wait()
		_ = srv.Close()
	}
	t.Cleanup(s.cleanup)
	return s
}

// call invokes one tool and decodes its structured output into out.
// A tool error fails the test with the reported message.
func (s *session) call(tool string, args map[string]any, out any) {
	s.t.Helper()
	res, err := s.client.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		s.t.Fatalf("CallTool %s: %v", tool, err)
	}
	if res.IsError {
		s.t.Fatalf("CallTool %s: tool error: %s", tool, contentText(s.t, res))
	}
	if err := json.Unmarshal(toolJSON(s.t, res), out); err != nil {
		s.t.Fatalf("CallTool %s: decoding output: %v", tool, err)
	}
}

// callError invokes one tool and expects a tool error, returning its
// message.
func (s *session) callError(tool string, args map[string]any) string {
	s.t.Helper()
	res, err := s.client.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		s.t.Fatalf("CallTool %s: %v", tool, err)
	}
	if !res.IsError {
		s.t.Fatalf("CallTool %s: expected a tool error, got %s", tool, contentText(s.t, res))
	}
	return contentText(s.t, res)
}

// contentText renders a result's text content for failure messages.
func contentText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return "(no content)"
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", res.Content[0])
	}
	return text.Text
}

// toolJSON extracts a result's structured output as JSON bytes.
func toolJSON(t *testing.T, res *sdk.CallToolResult) []byte {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("no structured content in result: %s", contentText(t, res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	return raw
}

func TestCreateSessionTool(t *testing.T) {
	tests := []struct {
		name         string
		args         map[string]any
		wantDeadline bool
	}{
		{
			name:         "perpetual session",
			args:         map[string]any{"namespace": "runs", "session": "alpha"},
			wantDeadline: false,
		},
		{
			name:         "timed session",
			args:         map[string]any{"namespace": "runs", "session": "beta", "ttl_seconds": 3600},
			wantDeadline: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession(t)
			var out struct {
				Namespace string  `json:"namespace"`
				Session   string  `json:"session"`
				Deadline  *string `json:"deadline"`
			}
			s.call("create_session", tt.args, &out)
			if out.Namespace != tt.args["namespace"] || out.Session != tt.args["session"] {
				t.Fatalf("echoed identity = %s/%s, want %v/%v",
					out.Namespace, out.Session, tt.args["namespace"], tt.args["session"])
			}
			if (out.Deadline != nil) != tt.wantDeadline {
				t.Fatalf("deadline = %v, want present: %v", out.Deadline, tt.wantDeadline)
			}
			if tt.wantDeadline {
				d, err := time.Parse(time.RFC3339, *out.Deadline)
				if err != nil {
					t.Fatalf("deadline %q is not RFC3339: %v", *out.Deadline, err)
				}
				if until := time.Until(d); until <= 0 || until > time.Hour {
					t.Fatalf("deadline %v is not within the requested hour", d)
				}
			}
		})
	}
}

func TestCreateSessionToolErrors(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "empty namespace",
			args: map[string]any{"namespace": "", "session": "alpha"},
			want: "non-empty",
		},
		{
			name: "zero ttl",
			args: map[string]any{"namespace": "runs", "session": "beta", "ttl_seconds": 0},
			want: "ttl",
		},
		{
			name: "lifecycle form mismatch",
			args: nil, // filled in the test body: re-create alpha as timed
			want: "exists",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession(t)
			if tt.args == nil {
				s.call("create_session", map[string]any{"namespace": "runs", "session": "alpha"}, new(any))
				tt.args = map[string]any{"namespace": "runs", "session": "alpha", "ttl_seconds": 60}
			}
			if got := s.callError("create_session", tt.args); !contains(got, tt.want) {
				t.Fatalf("error %q does not mention %q", got, tt.want)
			}
		})
	}
}

// contains is a tiny substring check so error assertions read as prose.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/magician336/elena"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioEndToEnd drives the full tool surface over the byte
// transport a stdio server actually uses: two pipes carry the
// newline-delimited JSON frames between server and client, the same
// framing the SDK's StdioTransport runs over the process streams. One
// pass exercises every tool in the order a runtime would use them.
func TestStdioEndToEnd(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(func() (*elena.Store, error) { return elena.Open(dir) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// serverIn: client writes, server reads. serverOut: server writes,
	// client reads.
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(ctx, &sdk.IOTransport{Reader: serverInR, Writer: serverOutW})
	}()

	client := sdk.NewClient(&sdk.Implementation{Name: "stdio-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, &sdk.IOTransport{Reader: serverOutR, Writer: serverInW}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	call := func(tool string, args map[string]any, out any) {
		t.Helper()
		res, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %s: %v", tool, err)
		}
		if res.IsError {
			t.Fatalf("CallTool %s: tool error: %s", tool, contentText(t, res))
		}
		if out == nil {
			return
		}
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal %s output: %v", tool, err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s output: %v", tool, err)
		}
	}

	// The whole tool set, in one working pass: a timed session, two
	// appended events, a checkpoint, a third event, a point-in-time
	// read at the baseline, the restore, and the observables.
	call("create_session", map[string]any{
		"namespace": "runs", "session": "alpha", "ttl_seconds": 3600,
	}, new(any))
	var first, second struct {
		Seq uint64 `json:"seq"`
	}
	call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "event one",
	}, &first)
	call("append_event", map[string]any{
		"namespace": "runs", "session": "alpha", "payload": "event two",
	}, &second)
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seqs = %d, %d; want 1, 2", first.Seq, second.Seq)
	}

	var checkpoint struct {
		CheckpointID uint64 `json:"checkpoint_id"`
	}
	call("create_checkpoint", map[string]any{}, &checkpoint)

	var past readOut
	call("read_trajectory", map[string]any{
		"namespace": "runs", "session": "alpha", "upto_seq": 1,
	}, &past)
	if len(past.Events) != 1 || past.Events[0].Payload != "event one" {
		t.Fatalf("point-in-time read = %+v; want only event one", past.Events)
	}

	var restored struct {
		Trajectories []struct {
			ReachedSeq uint64 `json:"reached_seq"`
		} `json:"trajectories"`
	}
	call("restore_checkpoint", map[string]any{
		"checkpoint_id": checkpoint.CheckpointID,
	}, &restored)
	if len(restored.Trajectories) != 1 || restored.Trajectories[0].ReachedSeq != 2 {
		t.Fatalf("restored baseline = %+v; want one line at seq 2", restored.Trajectories)
	}

	var stats struct {
		LiveEntries uint64 `json:"live_entries"`
	}
	call("stats", map[string]any{}, &stats)
	if stats.LiveEntries == 0 {
		t.Fatal("live entries is zero with stored events")
	}

	// Shut down from the client side: the server session ends, Serve
	// returns, the store closes.
	if err := clientSession.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	_ = serverInW.Close()
	_ = serverOutR.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the client closed")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("server Close: %v", err)
	}
}

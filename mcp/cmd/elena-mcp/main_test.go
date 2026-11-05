package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no directory", args: nil, want: "no store directory"},
		{name: "unknown flag", args: []string{"-bogus"}, want: "flag provided but not defined"},
		{name: "positional argument", args: []string{"-dir", "x", "extra"}, want: "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ELENA_MCP_DIR", "")
			var stderr bytes.Buffer
			code := Run(context.Background(), tt.args, &bytes.Buffer{}, &bytes.Buffer{}, &stderr)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !bytes.Contains(stderr.Bytes(), []byte(tt.want)) {
				t.Fatalf("stderr %q does not mention %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestRunOpenFailure(t *testing.T) {
	// A plain file is not a store directory: Open fails, the process
	// says so on stderr and exits 1.
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"-dir", file}, &bytes.Buffer{}, &bytes.Buffer{}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("no failure message on stderr")
	}
}

// runServer starts Run over pipes the way the process runs over its
// stdio streams, and returns a client session speaking to it plus the
// pipes' ends the test owns.
func runServer(t *testing.T, args []string) (*sdk.ClientSession, io.WriteCloser, io.ReadCloser) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	exited := make(chan int, 1)
	go func() {
		exited <- Run(ctx, args, stdinR, stdoutW, &bytes.Buffer{})
		cancel()
	}()
	client := sdk.NewClient(&sdk.Implementation{Name: "run-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, &sdk.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after the client closed")
		}
	})
	return clientSession, stdinW, stdoutR
}

func TestRunServesStoreOverStdio(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	clientSession, _, _ := runServer(t, []string{"-dir", dir})

	res, err := clientSession.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "append_event",
		Arguments: map[string]any{"namespace": "runs", "session": "alpha", "payload": "event one"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("append_event failed: %v", res.Content)
	}
	if got, ok := res.StructuredContent.(map[string]any)["seq"]; !ok || got.(float64) != 1 {
		t.Fatalf("seq = %v, want 1", res.StructuredContent)
	}
}

func TestRunServesStoreFromEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	t.Setenv("ELENA_MCP_DIR", dir)
	clientSession, _, _ := runServer(t, nil)

	res, err := clientSession.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "create_session",
		Arguments: map[string]any{"namespace": "runs", "session": "alpha"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_session failed: %v", res.Content)
	}
}

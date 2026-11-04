package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// failingWriter rejects every write, standing in for a closed pipe
// downstream of the command's output.
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("pipe closed") }

func TestRunCommandsPropagateWriteFailures(t *testing.T) {
	dir := seedDir(t)
	baseline := t.TempDir()
	checkpointAfterKill(t, baseline)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"status", []string{"status", dir}},
		{"dump", []string{"dump", dir}},
		{"merge", []string{"merge", dir}},
		{"checkpoint", []string{"checkpoint", dir}},
		{"checkpoint-list", []string{"checkpoint", "list", baseline}},
		{"restore", []string{"restore", baseline}},
	} {
		var stderr bytes.Buffer

		code := Run(tc.args, failingWriter{}, &stderr)

		if code != 1 {
			t.Errorf("%s with a closed pipe: exit code = %d, want 1", tc.name, code)
		}
		if !strings.Contains(stderr.String(), "pipe closed") {
			t.Errorf("%s with a closed pipe: stderr = %q, want the write failure", tc.name, stderr.String())
		}
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/magician336/elena"
)

func TestRunDumpWholeStore(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"dump", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"namespace alpha\n",
		"namespace beta\n",
		"  session runner\n",
		"  session scratch\n",
		"  session empty\n",
		`"first"`,
		`"second"`,
		`"note"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q;\ngot:\n%s", want, out)
		}
	}
	// Sequence numbers, sizes, and order: the runner trajectory reads
	// back in append order, the scratch event follows, the empty
	// session carries no event lines.
	i1 := strings.Index(out, "event #1")
	i2 := strings.Index(out, "event #2")
	iNote := strings.Index(out, `"note"`)
	if i1 < 0 || i2 < 0 || iNote < 0 || i1 >= i2 || i2 >= iNote {
		t.Errorf("dump events out of order (i1=%d i2=%d iNote=%d):\n%s", i1, i2, iNote, out)
	}
	for _, want := range []string{
		"event #1  ", "event #2  ", " 5 bytes ", " 6 bytes ", " 4 bytes ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump event line missing %q;\ngot:\n%s", want, out)
		}
	}
	if strings.Count(out, "event #") != 3 {
		t.Errorf("dump prints %d event lines, want 3:\n%s", strings.Count(out, "event #"), out)
	}
	assertSignalLines(t, out)
}

func TestRunDumpOneNamespace(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"dump", dir, "alpha"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "namespace alpha\n") {
		t.Errorf("dump alpha missing its namespace line:\n%s", out)
	}
	if strings.Contains(out, "beta") || strings.Contains(out, "empty") {
		t.Errorf("dump alpha leaked the beta namespace:\n%s", out)
	}
	assertSignalLines(t, out)
}

func TestRunDumpOneTrajectory(t *testing.T) {
	dir := seedDir(t)
	var stdout, stderr bytes.Buffer

	code := Run([]string{"dump", dir, "alpha", "runner"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"namespace alpha\n", "  session runner\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump trajectory missing %q;\ngot:\n%s", want, out)
		}
	}
	for _, absent := range []string{"scratch", `"note"`, "empty"} {
		if strings.Contains(out, absent) {
			t.Errorf("dump trajectory leaked %q:\n%s", absent, out)
		}
	}
	if strings.Count(out, "event #") != 2 {
		t.Errorf("dump trajectory prints %d event lines, want 2:\n%s", strings.Count(out, "event #"), out)
	}
	assertSignalLines(t, out)
}

func TestRunDumpUnknownTargetsFail(t *testing.T) {
	dir := seedDir(t)
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"dump", dir, "gamma"}, `namespace "gamma" not stored`},
		{[]string{"dump", dir, "alpha", "ghost"}, `session "ghost" not stored in namespace "alpha"`},
	} {
		var stdout, stderr bytes.Buffer

		code := Run(tc.args, &stdout, &stderr)

		if code != 1 {
			t.Fatalf("Run(%v) exit code = %d, want 1", tc.args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%v) stdout = %q, want empty", tc.args, stdout.String())
		}
		if !strings.Contains(stderr.String(), tc.wantErr) {
			t.Errorf("Run(%v) stderr = %q, want %q", tc.args, stderr.String(), tc.wantErr)
		}
	}
}

func TestRunDumpClipsLongPayloads(t *testing.T) {
	dir := t.TempDir()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sess, err := store.CreateSession("wide", "blob")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := sess.Append(bytes.Repeat([]byte("x"), 100)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"dump", dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, strings.Repeat("x", previewWidth)+`" ...`) {
		t.Errorf("dump preview not clipped to %d bytes with the marker:\n%s", previewWidth, out)
	}
	if strings.Contains(out, strings.Repeat("x", previewWidth+1)) {
		t.Errorf("dump preview shows more than %d bytes:\n%s", previewWidth, out)
	}
	assertSignalLines(t, out)
}

func TestRunDumpArgCountIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"dump"}, {"dump", "a", "b", "c", "d"}} {
		var stdout, stderr bytes.Buffer

		code := Run(args, &stdout, &stderr)

		if code != 2 {
			t.Fatalf("Run(%v) exit code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "command dump wants 1 to 3 arguments: <dir> [namespace [session]]") {
			t.Errorf("Run(%v) stderr = %q, want the usage message", args, stderr.String())
		}
	}
}

package recovery

import (
	"os"
	"testing"
)

// TestMain is the reexec entry of the recovery rig: when the
// writer-child environment variable is set, this process is the child
// writer and never runs any test — it appends the schedule through the
// public elena API, acks on stdout, and blocks until the injected
// kill lands. See writer.go for the protocol and rig.go for why the
// rig kills a real process.
func TestMain(m *testing.M) {
	if IsWriterChild() {
		os.Exit(RunWriterChild(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// maybeSkipKill9 skips the rig's kill -9 scenarios when the shared
// marker disables them, for environments that cannot run child
// processes.
func maybeSkipKill9(t *testing.T) {
	t.Helper()
	if kill9Disabled(os.Getenv) {
		t.Skipf("kill -9 harness disabled via %s", skipKill9Env)
	}
}

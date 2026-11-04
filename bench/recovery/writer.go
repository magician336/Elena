package recovery

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"elena"
)

// Environment variables of the writer-child protocol. The mode
// variable is set on the child only; the rest carry the Spec. The skip
// marker is 0001's, shared with the engine and semantic kill -9
// harnesses: one variable disables every kill -9 scenario.
const (
	writerChildModeEnv       = "ELENA_BENCH_RECOVERY_WRITER_CHILD"
	writerChildDirEnv        = "ELENA_BENCH_RECOVERY_WRITER_DIR"
	writerChildSessionsEnv   = "ELENA_BENCH_RECOVERY_WRITER_SESSIONS"
	writerChildEventsEnv     = "ELENA_BENCH_RECOVERY_WRITER_EVENTS"
	writerChildSeedEnv       = "ELENA_BENCH_RECOVERY_WRITER_SEED"
	writerChildPayloadEnv    = "ELENA_BENCH_RECOVERY_WRITER_PAYLOAD_SIZE"
	skipKill9Env             = "ELENA_TEST_SKIP_KILL9"
	writerNamespace          = "recovery"
	writerChildStdinHoldNote = "stdin closed before the kill — the parent vanished"
)

// IsWriterChild reports whether this process was reexeced as the rig's
// writer child. Whoever calls Measure reexeces their own process image
// for the child, so every entry point that reaches Measure — the
// package's own test binary, the recovery CLI — must check this first
// and switch to RunWriterChild before doing anything else, the same
// reexec contract the engine and semantic kill -9 harnesses run under.
func IsWriterChild() bool {
	return os.Getenv(writerChildModeEnv) != ""
}

// RunWriterChild is the writer-child main: it decodes the spec from
// the environment, appends the whole schedule through the public elena
// API — one ack line per append that has returned, under the store's
// default write-synchronized regime, so an acknowledged event is
// durable the moment the parent reads the line — prints the hold
// marker, and then blocks reading stdin, holding the store open with
// no shutdown code running, until the parent's SIGKILL lands. A stdin
// read that returns at all — a parent that vanished without killing —
// is a failure exit. Failures print to stderr and return a non-zero
// code so the parent reports them verbatim.
func RunWriterChild(stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "recovery writer child:", err)
		return 1
	}

	spec, err := writerSpecFromEnv()
	if err != nil {
		return fail(err)
	}
	st, err := elena.Open(spec.Dir)
	if err != nil {
		return fail(fmt.Errorf("open %q: %w", spec.Dir, err))
	}
	ack := func(a Ack) error {
		if _, err := fmt.Fprintf(stdout, "A %d %d\n", a.Session, a.Seq); err != nil {
			return fmt.Errorf("ack of %+v: %w", a, err)
		}
		return nil
	}
	if err := appendSchedule(st, spec, ack); err != nil {
		return fail(err)
	}
	if _, err := fmt.Fprintf(stdout, "W %d\n", spec.Events); err != nil {
		return fail(fmt.Errorf("hold marker: %w", err))
	}

	// Hold: block until the injected kill arrives. The read cannot
	// return while the parent lives — the parent holds the write end
	// of this pipe — so any return means the parent is gone.
	one := make([]byte, 1)
	_, err = stdin.Read(one)
	return fail(fmt.Errorf("%s: %v", writerChildStdinHoldNote, err))
}

// appendSchedule appends every schedule event through the store and
// acknowledges each returned append in order. Event e goes to session
// e%Sessions as its (e/Sessions+1)-th event; a sequence number that
// disagrees with that arithmetic fails the run loudly — the parent
// predicts every line from the same arithmetic.
func appendSchedule(st *elena.Store, spec Spec, ack func(Ack) error) error {
	sessions := make([]*elena.Session, spec.Sessions)
	for i := range sessions {
		sess, err := st.CreateSession(writerNamespace, fmt.Sprintf("s%d", i))
		if err != nil {
			return fmt.Errorf("create session s%d: %w", i, err)
		}
		sessions[i] = sess
	}
	for e := 0; e < spec.Events; e++ {
		idx := e % spec.Sessions
		seq, err := sessions[idx].Append(spec.payloadAt(e))
		if err != nil {
			return fmt.Errorf("append %d to s%d: %w", e, idx, err)
		}
		if want := uint64(e/spec.Sessions + 1); seq != want {
			return fmt.Errorf("append %d to s%d returned sequence %d, want %d", e, idx, seq, want)
		}
		if err := ack(Ack{Session: idx, Seq: seq}); err != nil {
			return err
		}
	}
	return nil
}

// writerChildEnv returns the child environment: the parent's plus the
// encoded spec and the mode switch.
func writerChildEnv(spec Spec) []string {
	return append(os.Environ(),
		writerChildModeEnv+"=1",
		writerChildDirEnv+"="+spec.Dir,
		writerChildSessionsEnv+"="+strconv.Itoa(spec.Sessions),
		writerChildEventsEnv+"="+strconv.Itoa(spec.Events),
		writerChildSeedEnv+"="+strconv.FormatInt(spec.Seed, 10),
		writerChildPayloadEnv+"="+strconv.Itoa(spec.PayloadSize),
	)
}

// writerSpecFromEnv decodes the Spec the parent encoded into the
// child's environment; KillAfterAcks is parent-side only and never
// crosses the boundary.
func writerSpecFromEnv() (Spec, error) {
	spec := Spec{Dir: os.Getenv(writerChildDirEnv)}
	var err error
	if spec.Sessions, err = strconv.Atoi(os.Getenv(writerChildSessionsEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerChildSessionsEnv, err)
	}
	if spec.Events, err = strconv.Atoi(os.Getenv(writerChildEventsEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerChildEventsEnv, err)
	}
	seed, err := strconv.ParseInt(os.Getenv(writerChildSeedEnv), 10, 64)
	if err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerChildSeedEnv, err)
	}
	spec.Seed = seed
	if spec.PayloadSize, err = strconv.Atoi(os.Getenv(writerChildPayloadEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerChildPayloadEnv, err)
	}
	if err := spec.Validate(); err != nil {
		return spec, fmt.Errorf("writer spec from env: %w", err)
	}
	return spec, nil
}

// kill9Disabled reports whether the shared skip marker disables the
// kill -9 scenarios in this environment. The marker is deliberately
// unset by default — this machine and the Linux CI both run every
// kill -9 scenario on each invocation.
func kill9Disabled(getenv func(string) string) bool {
	return getenv(skipKill9Env) != ""
}

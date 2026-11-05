package recovery

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"elena"
)

// Result carries one rig run's outcome: the dataset shape for the
// report axis, how many events were acknowledged when the kill landed,
// and the recovery timing segments. The timing convention, declared
// once here and printed with every report:
//
//   - KillToOpen spans SIGKILL delivery to Open returning — it
//     includes the killed child's teardown and reap, because recovery
//     begins when the fault lands, not when a host chooses to look.
//   - OpenToFirstRead spans Open returning to the first acknowledged
//     event reading back byte-exact through the public API.
//   - KillToFirstRead is their exact sum: kill to first readable.
//
// No engine or library timing API exists behind these numbers — the
// rig watches the public surface from outside the process under fault,
// which is the whole point of the instrument.
type Result struct {
	// Events, PayloadSize, and Sessions describe the schedule that ran;
	// TargetBytes is its dataset size, the tier axis of the report.
	Events      int
	PayloadSize int
	Sessions    int
	TargetBytes int64

	// AckedEvents is how many events the parent watched the child
	// acknowledge before the kill landed — in-flight lines already in
	// the pipe count, the child had watched those appends return.
	AckedEvents int

	// KillToOpen, OpenToFirstRead, and KillToFirstRead are the timing
	// segments declared above.
	KillToOpen      time.Duration
	OpenToFirstRead time.Duration
	KillToFirstRead time.Duration
}

// Measure runs one recovery measurement for spec and returns its
// result: the writer child appends the whole schedule through the
// public elena API, acknowledging every event; the parent watches the
// ack lines, kills the child with SIGKILL — at the n-th ack for a
// mid-run spec, at the hold marker otherwise — then times the reopen
// and proves the kill -9 correctness bar on the reopened directory:
// every acknowledged event survives with its exact payload, the
// trajectories read as hole-free runs at least as long as their
// acknowledged parts, and the recovered store accepts and durably
// keeps a post-kill append. Any violation is an error — the numbers
// only count for a store that lost nothing.
//
// The calling process is reexeced as the writer child, so any entry
// point that reaches Measure must honor IsWriterChild first (see
// writer.go). The measurement itself opens the store with the
// background sweep off, like the bench harness's elena driver: no
// timed window includes background reclamation work.
func Measure(spec Spec) (Result, error) {
	if err := spec.Validate(); err != nil {
		return Result{}, err
	}
	acked, killed, err := runWriterUntilKill(spec)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		Events:      spec.Events,
		PayloadSize: spec.PayloadSize,
		Sessions:    spec.Sessions,
		TargetBytes: spec.TargetBytes(),
		AckedEvents: len(acked),
	}

	ackedMax := ackedMaxSequence(acked, spec.Sessions)
	st, err := elena.Open(spec.Dir, elena.WithSweepDisabled())
	if err != nil {
		return Result{}, fmt.Errorf("reopen after kill -9: %w", err)
	}
	openReturned := time.Now()
	res.KillToOpen = openReturned.Sub(killed)

	// First read: the newest acknowledged event of the writer's first
	// session, read back byte-exact — recovery is done when the store
	// serves acknowledged data again.
	first, err := st.CreateSession(writerNamespace, writerSessionName(0))
	if err != nil {
		closeMeasured(st)
		return Result{}, fmt.Errorf("attach s0 after kill -9: %w", err)
	}
	ev, err := first.Event(ackedMax[0])
	if err != nil {
		closeMeasured(st)
		return Result{}, fmt.Errorf("first read of acked event s0#%d failed: %w", ackedMax[0], err)
	}
	if want := spec.payloadAt(eventIndex(0, ackedMax[0], spec.Sessions)); !bytes.Equal(ev.Payload, want) {
		closeMeasured(st)
		return Result{}, fmt.Errorf("first read of acked event s0#%d returned a corrupted payload", ackedMax[0])
	}
	firstRead := time.Now()
	res.OpenToFirstRead = firstRead.Sub(openReturned)
	res.KillToFirstRead = firstRead.Sub(killed)

	if err := verifyAckedSurvive(st, spec, ackedMax); err != nil {
		closeMeasured(st)
		return Result{}, err
	}
	if err := verifyPostKillWrite(st, spec.Dir, ackedMax); err != nil {
		closeMeasured(st) // a no-op close if the failure was the close itself
		return Result{}, err
	}
	return res, nil
}

// runWriterUntilKill runs the writer child for spec and returns every
// ack the parent observed plus the moment the kill was delivered. The
// child reexeces this process image in writer mode; the observation
// and kill decision live in scanAcks, and the reap check in
// waitWriterChild.
func runWriterUntilKill(spec Spec) ([]Ack, time.Time, error) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = writerChildEnv(spec)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pipe writer child stdin: %w", err)
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close() // the child never started; release the write end
		return nil, time.Time{}, fmt.Errorf("pipe writer child stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close() // the child never started; release the write end
		return nil, time.Time{}, fmt.Errorf("start writer child: %w", err)
	}
	// No Close of stdin beyond the two paths above: every path past a
	// successful Start reaps the child through cmd.Wait, and Wait closes
	// the pipe's write end itself.

	var killed time.Time
	var killErr error
	acked, scanErr := scanAcks(stdout, spec, func() {
		killed = time.Now()
		killErr = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
	})
	if scanErr != nil {
		// A broken stream leaves a live child holding its stdin: kill
		// and reap it, then report the observation error — the only
		// error that explains the run.
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, time.Time{}, scanErr
	}
	if err := waitWriterChild(cmd, killErr, stderr); err != nil {
		return nil, time.Time{}, err
	}
	return acked, killed, nil
}

// scanAcks reads the child's stdout line protocol and returns every
// acknowledged event: each 'A' line is validated against the predicted
// stream and counted, the 'W' hold marker against the schedule.
// onKill fires exactly once — at the n-th ack for a mid-run spec, at
// the hold marker otherwise — and lines already in flight after it
// still count: the child had watched those appends return before
// dying. A stream that ends without reaching the kill point is an
// error: no fault was injected.
func scanAcks(r io.Reader, spec Spec, onKill func()) ([]Ack, error) {
	expected := AckStream(spec)
	var acked []Ack
	killed := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "W" {
			held, err := strconv.Atoi(fields[1])
			if err != nil || held != spec.Events {
				return nil, fmt.Errorf("writer child hold marker %q disagrees with the %d-event schedule",
					line, spec.Events)
			}
			if spec.KillAfterAcks == 0 && !killed {
				killed = true
				onKill()
			}
			continue
		}
		ack, err := parseAckLine(line)
		if err != nil {
			return nil, fmt.Errorf("writer child: %w", err)
		}
		i := len(acked)
		if i >= len(expected) || ack != expected[i] {
			return nil, fmt.Errorf("writer child ack #%d = %+v, want the predicted %+v — schedules diverged",
				i+1, ack, expected[min(i, len(expected)-1)])
		}
		acked = append(acked, ack)
		if spec.KillAfterAcks > 0 && !killed && len(acked) == spec.KillAfterAcks {
			killed = true
			onKill()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading ack lines: %w", err)
	}
	if !killed {
		return nil, fmt.Errorf("writer child finished without reaching the kill point — no fault was injected")
	}
	return acked, nil
}

// waitWriterChild reaps the child and verifies it died from the
// injected SIGKILL. Anything else — a clean exit, a different signal,
// a crash with stderr output — is an error: a run without a real
// kill -9 proves nothing.
func waitWriterChild(cmd *exec.Cmd, killErr error, stderr *bytes.Buffer) error {
	waitErr := cmd.Wait()
	if waitErr == nil {
		return fmt.Errorf("writer child exited cleanly; no fault was injected (kill never landed)")
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return fmt.Errorf("writer child wait error: %w\nchild stderr:\n%s", waitErr, stderr)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if killErr != nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		return fmt.Errorf("writer child did not die from the injected SIGKILL: wait=%v kill=%v\nchild stderr:\n%s",
			waitErr, killErr, stderr)
	}
	return nil
}

// ackedMaxSequence returns each session's highest acknowledged
// sequence number, zero for sessions without acknowledged events.
// Acks for one session arrive in sequence order — appends are serial
// per session — so the max bounds a complete acknowledged run.
func ackedMaxSequence(acked []Ack, sessions int) []uint64 {
	max := make([]uint64, sessions)
	for _, ack := range acked {
		if ack.Seq > max[ack.Session] {
			max[ack.Session] = ack.Seq
		}
	}
	return max
}

// eventIndex maps (session, sequence) back to the schedule position:
// event e goes to session e%Sessions as its (e/Sessions+1)-th event.
func eventIndex(session int, seq uint64, sessions int) int {
	return int(seq-1)*sessions + session
}

// verifyAckedSurvive proves the kill -9 correctness bar on the
// reopened store, the same assertion shape as the repository's kill -9
// suites: for every session, every sequence number of its
// acknowledged run — 1 through its acknowledged maximum — reads back
// present with the exact dataset payload. A missing event is a lost
// acknowledgment; a differing payload is a torn write; both fail the
// measurement.
func verifyAckedSurvive(st *elena.Store, spec Spec, ackedMax []uint64) error {
	for s, top := range ackedMax {
		sess, err := st.CreateSession(writerNamespace, writerSessionName(s))
		if err != nil {
			return fmt.Errorf("attach s%d after kill -9: %w", s, err)
		}
		for seq := uint64(1); seq <= top; seq++ {
			ev, err := sess.Event(seq)
			if err != nil {
				return fmt.Errorf("acked event s%d#%d lost after kill -9 and reopen: %w", s, seq, err)
			}
			if want := spec.payloadAt(eventIndex(s, seq, spec.Sessions)); !bytes.Equal(ev.Payload, want) {
				return fmt.Errorf("acked event s%d#%d after kill -9 = %d payload bytes, want the exact acked %d bytes",
					s, seq, len(ev.Payload), len(want))
			}
		}
	}
	return nil
}

// verifyPostKillWrite proves the recovered store stays writable and
// keeps a fresh append across a clean close and reopen — recovery that
// cannot take writes would be a hollow number. The store it was given
// is closed along the way: a reopened store cannot verify through a
// closed one.
func verifyPostKillWrite(st *elena.Store, dir string, ackedMax []uint64) error {
	sess, err := st.CreateSession(writerNamespace, writerSessionName(0))
	if err != nil {
		return fmt.Errorf("attach s0 for the post-kill append: %w", err)
	}
	seq, err := sess.Append([]byte("post-kill"))
	if err != nil {
		return fmt.Errorf("append after kill -9 and reopen: %w", err)
	}
	if seq <= ackedMax[0] {
		return fmt.Errorf("post-kill append took sequence %d, at or below the acknowledged %d", seq, ackedMax[0])
	}
	if err := st.Close(); err != nil {
		return fmt.Errorf("close after the post-kill append: %w", err)
	}
	reopened, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return fmt.Errorf("reopen after the post-kill append: %w", err)
	}
	s0, err := reopened.CreateSession(writerNamespace, writerSessionName(0))
	if err != nil {
		closeMeasured(reopened)
		return fmt.Errorf("attach s0 after the second reopen: %w", err)
	}
	ev, err := s0.Event(seq)
	if err != nil || string(ev.Payload) != "post-kill" {
		closeMeasured(reopened)
		return fmt.Errorf("post-kill append s0#%d after reopen = %q, %v; want \"post-kill\", nil", seq, ev.Payload, err)
	}
	if err := reopened.Close(); err != nil {
		return fmt.Errorf("close after the second reopen: %w", err)
	}
	return nil
}

// closeMeasured closes a store whose verification already failed; the
// measurement result is discarded either way, so only the close error
// itself could add noise, and it cannot.
func closeMeasured(st *elena.Store) {
	_ = st.Close()
}

// SameMagnitude reports whether two durations agree within one order
// of magnitude — the stability bar the rig's repeated tier runs are
// held to. Non-positive durations never agree: a timing segment that
// did not elapse is not a measurement.
func SameMagnitude(a, b time.Duration) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	if a > b {
		a, b = b, a
	}
	return b <= 10*a
}

// writerSessionName renders session index i as the child's session
// name inside the rig namespace.
func writerSessionName(i int) string {
	return "s" + strconv.Itoa(i)
}

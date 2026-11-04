package elena_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"elena"
)

// listNamespaces returns the store's namespaces and fails the test on
// error.
func listNamespaces(t *testing.T, st *elena.Store) []string {
	t.Helper()
	namespaces, err := st.Namespaces()
	if err != nil {
		t.Fatalf("Namespaces() failed: %v", err)
	}
	return namespaces
}

// Namespaces must list every namespace of the store, sorted, and the
// empty store must list none.
func TestNamespacesListsNamespacesSorted(t *testing.T) {
	st := openStore(t)
	if got := listNamespaces(t, st); len(got) != 0 {
		t.Fatalf("Namespaces() of a fresh store = %v, want empty", got)
	}

	createSession(t, st, "beta", "one")
	createSession(t, st, "alpha", "two")
	createSession(t, st, "alpha", "three")

	got := listNamespaces(t, st)
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("Namespaces() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Namespaces() = %v, want %v", got, want)
		}
	}
}

// A namespace whose only session was timed keeps no live session once
// the TTL elapses, so Namespaces must omit it: the namespace is
// discoverable exactly as long as it holds something to find.
func TestNamespacesOmitsNamespaceWhoseOnlySessionWasTimed(t *testing.T) {
	st := openStore(t)
	const ttl = 400 * time.Millisecond
	createTimedSession(t, st, "runs", "alpha", ttl)

	if got := listNamespaces(t, st); len(got) != 1 || got[0] != "runs" {
		t.Fatalf("Namespaces() before the deadline = %v, want [runs]", got)
	}
	waitForTTLElapse(ttl)
	if got := listNamespaces(t, st); len(got) != 0 {
		t.Fatalf("Namespaces() after the deadline = %v, want empty", got)
	}
}

// listSessions returns the live sessions of namespace and fails the
// test on error.
func listSessions(t *testing.T, st *elena.Store, namespace string) []elena.SessionInfo {
	t.Helper()
	sessions, err := st.Sessions(namespace)
	if err != nil {
		t.Fatalf("Sessions(%q) failed: %v", namespace, err)
	}
	return sessions
}

// Sessions must list the live sessions of a namespace, sorted by ID,
// with each session's deadline: the zero time for a perpetual session,
// the recorded deadline for a timed one.
func TestSessionsListsLiveSessionsWithDeadlines(t *testing.T) {
	st := openStore(t)
	const ttl = 2 * time.Second
	created := time.Now()
	createSession(t, st, "runs", "keeper")
	createTimedSession(t, st, "runs", "temp", ttl)
	createSession(t, st, "other", "elsewhere")

	sessions := listSessions(t, st, "runs")
	if len(sessions) != 2 {
		t.Fatalf("Sessions(%q) returned %d sessions, want 2", "runs", len(sessions))
	}
	if sessions[0].ID != "keeper" || sessions[1].ID != "temp" {
		t.Fatalf("Sessions(%q) IDs = [%s %s], want [keeper temp]", "runs", sessions[0].ID, sessions[1].ID)
	}
	if !sessions[0].Deadline.IsZero() {
		t.Errorf("perpetual session keeper Deadline = %v, want the zero time", sessions[0].Deadline)
	}
	slack := time.Minute
	if d := sessions[1].Deadline; d.Before(created) || d.After(created.Add(ttl + slack)) {
		t.Errorf("timed session temp Deadline = %v, want within [%v, %v]", d, created, created.Add(ttl+slack))
	}
}

// Sessions of a namespace that holds no live session must read as
// empty, not as an error.
func TestSessionsOfNamespaceWithoutLiveSessionsIsEmpty(t *testing.T) {
	st := openStore(t)
	if got := listSessions(t, st, "unknown"); len(got) != 0 {
		t.Fatalf("Sessions of an unknown namespace = %v, want empty", got)
	}
}

// Sessions must reject an invalid namespace with ErrInvalidLabel, like
// every other namespace-taking call.
func TestSessionsRejectsInvalidNamespace(t *testing.T) {
	st := openStore(t)
	if _, err := st.Sessions(""); !errors.Is(err, elena.ErrInvalidLabel) {
		t.Fatalf("Sessions(%q) err = %v, want ErrInvalidLabel", "", err)
	}
	if _, err := st.Sessions(strings.Repeat("a", 481)); !errors.Is(err, elena.ErrInvalidLabel) {
		t.Fatalf("Sessions(oversized) err = %v, want ErrInvalidLabel", err)
	}
}

// Perpetual and timed sessions sharing a namespace must not disturb
// each other: both list before the deadline; after it only the
// perpetual session remains, its trajectory intact and the timed one's
// gone.
func TestPerpetualAndTimedSessionsShareNamespaceWithoutInterference(t *testing.T) {
	st := openStore(t)
	const ttl = 400 * time.Millisecond
	keeper := createSession(t, st, "runs", "keeper")
	temp := createTimedSession(t, st, "runs", "temp", ttl)
	for i := 0; i < 2; i++ {
		mustAppend(t, keeper, "kept")
		mustAppend(t, temp, "temporary")
	}

	if sessions := listSessions(t, st, "runs"); len(sessions) != 2 {
		t.Fatalf("Sessions before the deadline returned %d sessions, want 2", len(sessions))
	}

	waitForTTLElapse(ttl)

	sessions := listSessions(t, st, "runs")
	if len(sessions) != 1 || sessions[0].ID != "keeper" {
		t.Fatalf("Sessions after the deadline = %v, want [keeper]", sessions)
	}
	events, err := keeper.Events()
	if err != nil {
		t.Fatalf("perpetual Events() after the other session's deadline failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("perpetual Events() after the other session's deadline returned %d events, want 2", len(events))
	}
	events, err = temp.Events()
	if err != nil {
		t.Fatalf("timed Events() after the deadline failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("timed Events() after the deadline returned %d events, want 0", len(events))
	}
}

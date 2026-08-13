package vendors

import (
	"strings"
	"testing"
)

// A configured default must NOT silently answer for a session that no person
// has answered for.
//
// This is the regression test for a hazard found on 2026-08-03 while
// answering "how does Glider handle several projects at the same time?". The
// store keyed directories by PID, which isolates two sessions correctly. But
// Lookup fell back to ONE global default for any PID with no entry. Therefore
// a person who set a default while working on project alpha, and then opened
// a second CLI in project beta, got the first handoff of that beta session
// running in alpha -- with no question asked. A delegate could then edit the
// files of the wrong project.
func TestWorkspaceStore_DefaultDoesNotAnswerForAnUnknownSession(t *testing.T) {
	s := NewWorkspaceStore()
	s.SetDefault("C:/projects/alpha")
	s.Set(1001, "C:/projects/alpha")

	if dir, ok := s.Lookup(2002); ok {
		t.Fatalf("an unknown PID was answered with %q instead of being asked", dir)
	}
	// The default is still readable, so the question can offer it.
	if s.Default() != "C:/projects/alpha" {
		t.Fatalf("Default() = %q, want the configured default", s.Default())
	}
}

// Two sessions that each answered keep their own directories.
func TestWorkspaceStore_ParallelProjectsStaySeparate(t *testing.T) {
	s := NewWorkspaceStore()
	s.SetDefault("C:/projects/alpha")
	s.Set(1001, "C:/projects/alpha")
	s.Set(2002, "C:/projects/beta")

	if dir, _ := s.Lookup(1001); dir != "C:/projects/alpha" {
		t.Errorf("pid 1001 = %q", dir)
	}
	if dir, _ := s.Lookup(2002); dir != "C:/projects/beta" {
		t.Errorf("pid 2002 = %q", dir)
	}
}

// The question must name the default, so accepting it costs one reply and no
// typing of a path.
func TestAskForWorkspace_OffersTheConfiguredDefault(t *testing.T) {
	old := defaultWorkspaceStore.Default()
	t.Cleanup(func() { defaultWorkspaceStore.SetDefault(old) })

	defaultWorkspaceStore.SetDefault("")
	bare := askForWorkspace("agy")
	if !strings.Contains(bare, ". /workspace") {
		t.Errorf("the question must show the shortest usable reply: %q", bare)
	}
	if strings.Contains(bare, "Your default is") {
		t.Errorf("no default is set, so none should be offered: %q", bare)
	}

	defaultWorkspaceStore.SetDefault("C:/projects/alpha")
	withDefault := askForWorkspace("agy")
	if !strings.Contains(withDefault, `"C:/projects/alpha /workspace"`) {
		t.Errorf("the question must offer the default as a ready reply: %q", withDefault)
	}
	if !strings.Contains(withDefault, "cannot see which project") {
		t.Errorf("the question must say why it asks every session: %q", withDefault)
	}
}

// Two sessions in the SAME project are allowed to resolve to the same
// directory, and must. The 2026-08-03 change removed a silent default; it did
// not forbid sharing. Two claude windows open in one repo are one project.
func TestWorkspaceStore_TwoSessionsInOneProjectShareThatDirectory(t *testing.T) {
	s := NewWorkspaceStore()
	s.Set(1001, "C:/projects/alpha")
	s.Set(1002, "C:/projects/alpha") // second CLI, same repo, answered the same

	a, okA := s.Lookup(1001)
	b, okB := s.Lookup(1002)
	if !okA || !okB || a != b || a != "C:/projects/alpha" {
		t.Fatalf("two sessions in one project must share it: %q/%v, %q/%v", a, okA, b, okB)
	}
}

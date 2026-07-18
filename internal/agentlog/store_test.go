package agentlog

import "testing"

func TestPerInstanceIsolation(t *testing.T) {
	s := NewStore(8)
	s.Reset(ScopeHoop, "hoop-a")
	s.Reset(ScopeHoop, "hoop-b")
	s.Info(ScopeHoop, "hoop-a", "stage_start", "planner", nil)
	s.Info(ScopeHoop, "hoop-b", "stage_start", "actor", nil)
	s.Error(ScopeSwarm, "run-1", "worker", "fail", map[string]string{"role": "security"})

	a := s.Recent(ScopeHoop, "hoop-a", 50)
	b := s.Recent(ScopeHoop, "hoop-b", 50)
	sw := s.Recent(ScopeSwarm, "run-1", 50)

	if len(a) < 2 { // lifecycle + stage_start
		t.Fatalf("hoop-a entries=%d", len(a))
	}
	for _, e := range a {
		if e.InstanceID != "hoop-a" {
			t.Fatalf("leaked entry into hoop-a: %+v", e)
		}
		if e.Message == "actor" {
			t.Fatal("hoop-b message leaked into hoop-a")
		}
	}
	for _, e := range b {
		if e.InstanceID != "hoop-b" {
			t.Fatalf("bad id %+v", e)
		}
	}
	if len(sw) == 0 || sw[len(sw)-1].Message != "fail" {
		t.Fatalf("swarm log=%+v", sw)
	}
}

func TestResetFreshTimeline(t *testing.T) {
	s := NewStore(16)
	s.Info(ScopeHoop, "h1", "stage_start", "old", nil)
	s.Reset(ScopeHoop, "h1")
	got := s.Recent(ScopeHoop, "h1", 50)
	for _, e := range got {
		if e.Message == "old" {
			t.Fatal("old entries survived Reset")
		}
	}
	if len(got) == 0 || got[0].Kind != "lifecycle" {
		t.Fatalf("want lifecycle start, got %+v", got)
	}
}

func TestRingCap(t *testing.T) {
	s := NewStore(4)
	s.Reset(ScopeHoop, "h")
	for i := 0; i < 10; i++ {
		s.Info(ScopeHoop, "h", "tick", "x", nil)
	}
	got := s.Recent(ScopeHoop, "h", 100)
	if len(got) != 4 {
		t.Fatalf("len=%d want 4", len(got))
	}
}

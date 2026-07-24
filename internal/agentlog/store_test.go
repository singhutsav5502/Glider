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

func TestSeqAssignedAndUnique(t *testing.T) {
	s := NewStore(16)
	s.Info(ScopeHoop, "h1", "a", "one", nil)
	s.Info(ScopeHoop, "h1", "b", "two", nil)
	s.Info(ScopeSwarm, "r1", "c", "three", nil)
	a := s.Recent(ScopeHoop, "h1", 50)
	b := s.Recent(ScopeSwarm, "r1", 50)
	if len(a) < 2 || a[0].Seq == 0 || a[1].Seq == 0 {
		t.Fatalf("missing seq on hoop entries: %+v", a)
	}
	if a[0].Seq == a[1].Seq {
		t.Fatalf("duplicate seq %d", a[0].Seq)
	}
	if len(b) == 0 || b[0].Seq == 0 {
		t.Fatalf("missing seq on swarm: %+v", b)
	}
	seen := map[uint64]bool{}
	for _, e := range append(a, b...) {
		if seen[e.Seq] {
			t.Fatalf("seq %d not unique across scopes", e.Seq)
		}
		seen[e.Seq] = true
	}
}

func TestAfterCursor(t *testing.T) {
	s := NewStore(32)
	s.Info(ScopeHoop, "h1", "a", "one", nil)
	s.Info(ScopeHoop, "h1", "b", "two", nil)
	s.Info(ScopeHoop, "h1", "c", "three", nil)
	all := s.Recent(ScopeHoop, "h1", 50)
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d", len(all))
	}
	mid := all[0].Seq
	got := s.After(ScopeHoop, "h1", mid, 50)
	if len(got) != 2 {
		t.Fatalf("After(%d) len=%d want 2: %+v", mid, len(got), got)
	}
	if got[0].Seq <= mid || got[1].Seq <= mid {
		t.Fatalf("After returned seq <= afterSeq: %+v", got)
	}
	if got[0].Message != "two" || got[1].Message != "three" {
		t.Fatalf("order/content wrong: %+v", got)
	}
	if empty := s.After(ScopeHoop, "h1", all[2].Seq, 50); empty != nil {
		t.Fatalf("After at tip want nil, got %+v", empty)
	}
	if empty := s.After(ScopeHoop, "missing", 0, 50); empty != nil {
		t.Fatalf("missing id want nil, got %+v", empty)
	}
	// Limit caps oldest-first catch-up.
	limited := s.After(ScopeHoop, "h1", 0, 2)
	if len(limited) != 2 || limited[0].Message != "one" || limited[1].Message != "two" {
		t.Fatalf("limit=2 oldest-first: %+v", limited)
	}
	// Isolation: other instance not included.
	s.Info(ScopeHoop, "h2", "x", "other", nil)
	only := s.After(ScopeHoop, "h1", mid, 50)
	for _, e := range only {
		if e.InstanceID != "h1" {
			t.Fatalf("leaked: %+v", e)
		}
	}
}

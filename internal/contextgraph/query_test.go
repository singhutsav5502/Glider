package contextgraph

import "testing"

func TestQueryAndRelevancy(t *testing.T) {
	s := New("")
	s.Append(Event{Kind: EventLoopTick, TurnID: "t1", Attrs: map[string]string{"stage": "planner"}})
	s.Append(Event{Kind: EventFulfilledLocal, TurnID: "t1", Attrs: map[string]string{"ok": "1"}})
	s.RecordFact("t1", Fact{ID: "f1", Kind: "entity", Label: "planner", Provenance: ProvenanceExtracted})

	q := s.Query("t1", "planner", 10)
	if q == "" || !contains(q, "planner") {
		t.Fatalf("q=%s", q)
	}
	r := s.RelevancyScore("t1")
	if r < 0.4 || r > 1 {
		t.Fatalf("r=%v", r)
	}
	path := s.PathSummary("t1", "planner", "fulfilled")
	if path == "" {
		t.Fatal("empty path")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}

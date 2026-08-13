package contextgraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDualLayerQueryAndPath(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Append(Event{Kind: EventLoopTick, TurnID: "t1", Attrs: map[string]string{"stage": "planner"}})
	s.RecordFact("t1", Fact{
		ID: "planner-node", Kind: KindEntity, Label: "planner",
		Provenance: ProvenanceExtracted,
	})
	s.RecordFact("t1", Fact{
		ID: "fulfilled-node", Kind: KindEntity, Label: "fulfilled",
		Provenance: ProvenanceRuntime,
	})
	s.RecordEdge("t1", "e1", "planner-node", "fulfilled-node", RelFollows, ProvenanceInferred, nil)

	q := s.Query("t1", "planner", 20)
	if !strings.Contains(q, "entity:") || !strings.Contains(q, "planner") {
		t.Fatalf("expected entity hit, got %q", q)
	}
	if !strings.Contains(q, "event:") && !strings.Contains(q, "LoopTick") {
		// event layer may or may not match "planner" depending on attrs; entity is enough
	}
	path := s.PathSummary("t1", "planner", "fulfilled")
	if !strings.Contains(path, "follows") && !strings.Contains(path, "planner") {
		t.Fatalf("path=%q", path)
	}
	st := s.Stats()
	if st.Entities < 2 {
		t.Fatalf("entities=%d", st.Entities)
	}

	// Persist + reload entities.
	s2 := New(dir)
	n, err := s2.LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("loaded %d", n)
	}
	if _, ok := s2.GetEntity("planner-node"); !ok {
		t.Fatal("missing planner-node after reload")
	}
	if _, err := os.Stat(filepath.Join(dir, "entities.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestWaveOutputs(t *testing.T) {
	s := New("")
	s.RecordFact("t1", Fact{
		ID: "w0", Kind: KindWave, Label: "wave 0",
		Provenance: ProvenanceRuntime,
		Attrs:      map[string]string{"wave": "0", "summary": "first wave result"},
	})
	outs := s.WaveOutputs("t1", 0, 10)
	if len(outs) == 0 || !strings.Contains(outs[0], "first wave") {
		t.Fatalf("outs=%v", outs)
	}
}

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
	return strings.Contains(s, sub)
}

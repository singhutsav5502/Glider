package contextgraph_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextgraph"
)

func TestRecordHoopContextUpsertAndDigest(t *testing.T) {
	s := contextgraph.New("")
	turn := "loop:audit1"
	s.RecordHoopContext(turn, contextgraph.HoopKeyGoal, "Audit Unbrokify")
	s.RecordHoopContext(turn, contextgraph.HoopKeyClonePath, "runs/audit1/work/audit-target")
	s.RecordHoopContext(turn, contextgraph.HoopKeyPlan, "1) clone 2) fan-out")

	got, ok := s.LookupHoopContext(turn, "clone_path")
	if !ok || got != "runs/audit1/work/audit-target" {
		t.Fatalf("LookupHoopContext clone_path=%q ok=%v", got, ok)
	}
	got, ok = s.LookupHoopContext(turn, "GOAL")
	if !ok || !strings.Contains(got, "Unbrokify") {
		t.Fatalf("goal=%q ok=%v", got, ok)
	}

	// Upsert replaces value.
	s.RecordHoopContext(turn, contextgraph.HoopKeyClonePath, "runs/audit1/work/audit-target-v2")
	got, ok = s.LookupHoopContext(turn, "clone-path")
	if !ok || got != "runs/audit1/work/audit-target-v2" {
		t.Fatalf("upsert clone_path=%q ok=%v", got, ok)
	}

	digest := s.HoopContextDigest(turn)
	if !strings.Contains(digest, "clone_path:") || !strings.Contains(digest, "goal:") || !strings.Contains(digest, "plan:") {
		t.Fatalf("digest missing keys: %q", digest)
	}

	q := s.Query(turn, "clone_path", 8)
	if q == "" || strings.Contains(strings.ToLower(q), "no hits") {
		t.Fatalf("context_query-style Query miss: %q", q)
	}
}

func TestLookupHoopContextLegacyGoalFact(t *testing.T) {
	s := contextgraph.New("")
	turn := "loop:legacy"
	s.RecordFact(turn, contextgraph.Fact{
		ID:         "hoop-goal-legacy",
		Kind:       contextgraph.KindNote,
		Label:      "goal",
		Provenance: contextgraph.ProvenanceRuntime,
		Attrs:      map[string]string{"text": "legacy goal text"},
	})
	got, ok := s.LookupHoopContext(turn, "goal")
	if !ok || got != "legacy goal text" {
		t.Fatalf("legacy lookup=%q ok=%v", got, ok)
	}
}

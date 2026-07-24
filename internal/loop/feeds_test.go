package loop

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextgraph"
)

func TestIsFeedsEdge(t *testing.T) {
	if !isFeedsEdge(GraphEdge{Kind: "feeds", Source: "a", Target: "b"}) {
		t.Fatal("kind=feeds")
	}
	if !isFeedsEdge(GraphEdge{Kind: "flow", Label: "feeds", Source: "a", Target: "b"}) {
		t.Fatal("flow+label feeds")
	}
	if isFeedsEdge(GraphEdge{Kind: "flow", Source: "a", Target: "b"}) {
		t.Fatal("plain flow should not feed")
	}
}

func TestFeedSources(t *testing.T) {
	edges := []GraphEdge{
		{Kind: "flow", Source: "plan", Target: "act"},
		{Kind: "feeds", Source: "research", Target: "act"},
		{Kind: "flow", Label: "feeds", Source: "notes", Target: "act"},
		{Kind: "feeds", Source: "research", Target: "other"},
	}
	got := feedSources(edges, "act")
	if len(got) != 2 || got[0] != "research" || got[1] != "notes" {
		t.Fatalf("got %v", got)
	}
}

func TestRecordAndFeedsPromptBlock(t *testing.T) {
	g := contextgraph.New("")
	mgr := &Manager{Graph: g}
	st := &LoopState{
		Spec: LoopSpec{
			ID: "feed-hoop",
			GraphEdges: []GraphEdge{
				{Kind: "feeds", Source: "research", Target: "synth"},
				{Kind: "flow", Source: "research", Target: "synth"},
			},
			Stages: []StageSpec{
				{ID: "research", Kind: StageActor},
				{ID: "synth", Kind: StageActor},
			},
		},
		Artifacts: []string{"runs/feed-hoop/out/notes.md"},
		LiveStages: []StageOutcome{
			{ModuleID: "research", Summary: "Found 3 vulns in auth", Success: true},
		},
	}
	turn := "loop:feed-hoop"
	mgr.recordStageFeed(st, turn, "research", "Found 3 vulns in auth")

	if v, ok := g.LookupHoopContext(turn, hoopFeedKey("research")); !ok || !strings.Contains(v, "vulns") {
		t.Fatalf("hoop feed key: ok=%v v=%q", ok, v)
	}
	// RelFeeds edge should exist.
	ents := g.Entities(turn, 50)
	foundEdge := false
	for _, e := range ents {
		if e.Kind == contextgraph.KindEdge && e.Relation == contextgraph.RelFeeds {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Fatalf("expected RelFeeds edge among %+v", ents)
	}

	block := mgr.feedsPromptBlock(st, StageSpec{ID: "synth", Kind: StageActor})
	if !strings.Contains(block, "FEEDS:") || !strings.Contains(block, "research") || !strings.Contains(block, "vulns") {
		t.Fatalf("block=%q", block)
	}
	if !strings.Contains(block, "notes.md") {
		t.Fatalf("expected artifacts in block: %q", block)
	}
	// Consumer with no feeds inbound → empty.
	if got := mgr.feedsPromptBlock(st, StageSpec{ID: "research", Kind: StageActor}); got != "" {
		t.Fatalf("producer should not get feeds block: %q", got)
	}
}

func TestGraphEdgesNormalizeFeeds(t *testing.T) {
	s := LoopSpec{
		Goal:   "g",
		Stages: []StageSpec{{Kind: StageActor, ID: "a"}, {Kind: StageActor, ID: "b"}},
		GraphEdges: []GraphEdge{
			{Source: "a", Target: "b", Kind: "feeds"},
		},
	}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.GraphEdges[0].Kind != "feeds" {
		t.Fatalf("%+v", s.GraphEdges[0])
	}
}

// TestFeedsEdgeSkippedInMachineWalk ensures kind=feeds is data-only: FromLoopStages
// omits it from control transitions and WalkOrder / stageOrder follow flow only.
func TestFeedsEdgeSkippedInMachineWalk(t *testing.T) {
	spec := LoopSpec{
		ID:   "feeds-walk",
		Goal: "g",
		Stages: []StageSpec{
			{ID: "producer", Kind: StageActor},
			{ID: "consumer", Kind: StageActor},
			{ID: "critic", Kind: StageCritic},
		},
		GraphEdges: []GraphEdge{
			{ID: "f1", Source: "producer", Target: "consumer", Kind: "flow"},
			{ID: "feed1", Source: "producer", Target: "consumer", Kind: "feeds"},
			{ID: "f2", Source: "consumer", Target: "critic", Kind: "flow"},
		},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatal(err)
	}
	def, err := BuildMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Transitions) != 2 {
		t.Fatalf("want 2 flow transitions (feeds skipped), got %d: %+v", len(def.Transitions), def.Transitions)
	}
	for _, tr := range def.Transitions {
		if tr.Kind == "feeds" {
			t.Fatalf("feeds must not become a control transition: %+v", tr)
		}
	}
	ordered, _, err := stageOrderFromMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 {
		t.Fatalf("order len=%d want 3: %+v", len(ordered), ordered)
	}
	ids := []string{ordered[0].ID, ordered[1].ID, ordered[2].ID}
	if ids[0] != "producer" || ids[1] != "consumer" || ids[2] != "critic" {
		t.Fatalf("walk order=%v want producer→consumer→critic", ids)
	}
}

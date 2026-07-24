package swarm

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextkit"
)

func TestMergeResultsKeepsAuditSizedWorkerText(t *testing.T) {
	body := strings.Repeat("finding ", 2000) // ~16k chars
	ep := MergeResults([]Result{
		{WorkerID: "quality-0", Episode: contextkit.Episode{Summary: body, Tokens: 100}},
		{WorkerID: "security-1", Episode: contextkit.Episode{Summary: body, Tokens: 100}},
	})
	if !strings.Contains(ep.Summary, "finding") {
		t.Fatal("expected worker body in merge")
	}
	// Old 4k/worker cap would clip well under 10k total; raised caps keep far more.
	if len(ep.Summary) < 20000 {
		t.Fatalf("merge too aggressive: len=%d", len(ep.Summary))
	}
	if strings.Count(ep.Summary, "…") > 2 {
		t.Fatalf("unexpected heavy truncation markers: %q", ep.Summary[len(ep.Summary)-40:])
	}
}

func TestOrchestratorSummaryCapRaised(t *testing.T) {
	sum := strings.Repeat("x", orchestratorSummaryCap+100)
	got := OrchestratorSummary(contextkit.Episode{Summary: sum}, nil)
	if !strings.Contains(got, "…") {
		t.Fatal("expected hard-cap marker")
	}
	if len(got) < orchestratorSummaryCap {
		t.Fatalf("cap too low: %d", len(got))
	}
}

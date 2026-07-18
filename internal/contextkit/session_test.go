package contextkit_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/contextkit"
)

func TestSessionStoreEpisodeRingAndBudget(t *testing.T) {
	s := contextkit.NewStore(3)
	s.SetBudget("s1", 1000, 5000)
	s.SetStickyScope("s1", "turn_family")
	for i := 0; i < 5; i++ {
		s.RecordEpisode("s1", contextkit.Episode{
			ID:      string(rune('a' + i)),
			Summary: "step",
			Tokens:  10,
			Rule:    "Task Classifier Small-Local",
			Reason:  "small_offload",
			Role:    "exec",
		})
	}
	st := s.Get("s1")
	if len(st.Episodes) != 3 {
		t.Fatalf("episodes=%d want 3 (ring)", len(st.Episodes))
	}
	if st.Budget.SpentTokens != 50 {
		t.Fatalf("spent=%d", st.Budget.SpentTokens)
	}
	if st.Budget.Remaining() != 4950 {
		t.Fatalf("remaining=%d", st.Budget.Remaining())
	}
	if st.StickyScope != "turn_family" {
		t.Fatalf("sticky=%q", st.StickyScope)
	}
	if st.LastReason != "small_offload" {
		t.Fatalf("last_reason=%q", st.LastReason)
	}
}

func TestTurnBudgetOverSoft(t *testing.T) {
	b := contextkit.TurnBudget{SoftTokens: 100, HardTokens: 200, SpentTokens: 100}
	if !b.OverSoft() {
		t.Fatal("expected over soft")
	}
}

package loop

import (
	"fmt"
	"strings"

	"github.com/glider-ai/glider/internal/contextgraph"
)

const (
	edgeKindFeeds   = "feeds"
	feedSummaryCap  = 1200
	feedArtifactCap = 8
	feedBlockCap    = 3500
)

// isFeedsEdge reports whether e is a data-feed edge (kind=feeds, or flow labeled feeds).
// Feeds edges seed consumer prompts; they are not control-flow transitions.
func isFeedsEdge(e GraphEdge) bool {
	kind := strings.ToLower(strings.TrimSpace(e.Kind))
	label := strings.ToLower(strings.TrimSpace(e.Label))
	if kind == edgeKindFeeds {
		return true
	}
	// Alternate: flow + attr/label "feeds" (canvas reuse without new kind).
	if kind == "flow" && (label == edgeKindFeeds || label == "feed") {
		return true
	}
	return false
}

// feedSources returns stage ids that feed into consumerID via feeds edges.
func feedSources(edges []GraphEdge, consumerID string) []string {
	consumerID = strings.TrimSpace(consumerID)
	if consumerID == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range edges {
		if !isFeedsEdge(e) {
			continue
		}
		if strings.TrimSpace(e.Target) != consumerID {
			continue
		}
		src := strings.TrimSpace(e.Source)
		if src == "" || seen[src] {
			continue
		}
		seen[src] = true
		out = append(out, src)
	}
	return out
}

// hoopFeedKey is the RecordHoopContext key for a stage's last feed summary.
func hoopFeedKey(stageID string) string {
	stageID = strings.TrimSpace(stageID)
	if stageID == "" {
		return ""
	}
	return "feed_" + stageID
}

func stageEntityID(turnID, stageID string) string {
	turn := strings.TrimSpace(turnID)
	if turn == "" {
		turn = "global"
	}
	turn = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		case r == ':' || r == '/':
			return '-'
		default:
			return '_'
		}
	}, turn)
	return "hoop-stage-" + turn + "-" + strings.TrimSpace(stageID)
}

// recordStageFeed upserts the stage's last summary into contextgraph and writes
// RelFeeds edges for any feeds graph_edges leaving this stage.
func (m *Manager) recordStageFeed(st *LoopState, turnID, stageID, summary string) {
	if m == nil || st == nil || m.Graph == nil {
		return
	}
	stageID = strings.TrimSpace(stageID)
	summary = strings.TrimSpace(summary)
	if stageID == "" || summary == "" {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		turnID = "loop:" + st.Spec.ID
	}
	key := hoopFeedKey(stageID)
	m.Graph.RecordHoopContext(turnID, key, truncate(summary, feedSummaryCap))
	fromID := stageEntityID(turnID, stageID)
	m.Graph.RecordFact(turnID, contextgraph.Fact{
		ID:         fromID,
		Kind:       contextgraph.KindEpisode,
		Label:      "stage:" + stageID,
		Provenance: contextgraph.ProvenanceRuntime,
		Attrs: map[string]string{
			"stage":   stageID,
			"summary": truncate(summary, feedSummaryCap),
			"hoop":    st.Spec.ID,
		},
	})
	for _, e := range st.Spec.GraphEdges {
		if !isFeedsEdge(e) || strings.TrimSpace(e.Source) != stageID {
			continue
		}
		to := strings.TrimSpace(e.Target)
		if to == "" {
			continue
		}
		toID := stageEntityID(turnID, to)
		m.Graph.RecordFact(turnID, contextgraph.Fact{
			ID:         toID,
			Kind:       contextgraph.KindEpisode,
			Label:      "stage:" + to,
			Provenance: contextgraph.ProvenanceInferred,
			Attrs:      map[string]string{"stage": to, "hoop": st.Spec.ID},
		})
		m.Graph.RecordEdge(turnID, "", fromID, toID, contextgraph.RelFeeds,
			contextgraph.ProvenanceRuntime, map[string]string{
				"source_stage": stageID,
				"target_stage": to,
			})
	}
}

// lookupStageFeedSummary finds a producer stage's last summary from LiveStages,
// recent Outcomes, or RecordHoopContext feed_* keys.
func (m *Manager) lookupStageFeedSummary(st *LoopState, turnID, stageID string) string {
	stageID = strings.TrimSpace(stageID)
	if stageID == "" || st == nil {
		return ""
	}
	for i := len(st.LiveStages) - 1; i >= 0; i-- {
		s := st.LiveStages[i]
		if s.ModuleID == stageID && strings.TrimSpace(s.Summary) != "" {
			return strings.TrimSpace(s.Summary)
		}
	}
	for i := len(st.Outcomes) - 1; i >= 0; i-- {
		for j := len(st.Outcomes[i].Stages) - 1; j >= 0; j-- {
			s := st.Outcomes[i].Stages[j]
			if s.ModuleID == stageID && strings.TrimSpace(s.Summary) != "" {
				return strings.TrimSpace(s.Summary)
			}
		}
	}
	if m != nil && m.Graph != nil {
		if v, ok := m.Graph.LookupHoopContext(turnID, hoopFeedKey(stageID)); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// feedsPromptBlock builds a FEEDS section for the consumer stage from graph_edges.
func (m *Manager) feedsPromptBlock(st *LoopState, consumer ModuleSpec) string {
	if st == nil {
		return ""
	}
	consumerID := strings.TrimSpace(consumer.ID)
	if consumerID == "" {
		consumerID = string(consumer.Kind)
	}
	sources := feedSources(st.Spec.GraphEdges, consumerID)
	if len(sources) == 0 {
		return ""
	}
	turnID := "loop:" + st.Spec.ID
	var b strings.Builder
	b.WriteString("FEEDS:\n")
	b.WriteString("(producer stage summaries/artifacts seeded via graph_edges kind=feeds)\n")
	n := 0
	for _, src := range sources {
		sum := m.lookupStageFeedSummary(st, turnID, src)
		if sum == "" {
			continue
		}
		fmt.Fprintf(&b, "from %s:\n%s\n", src, truncate(sum, feedSummaryCap))
		n++
	}
	if n == 0 {
		return ""
	}
	if arts := st.Artifacts; len(arts) > 0 {
		b.WriteString("artifacts:\n")
		for i, a := range arts {
			if i >= feedArtifactCap {
				break
			}
			b.WriteString("- ")
			b.WriteString(a)
			b.WriteByte('\n')
		}
	}
	return truncate(strings.TrimSpace(b.String()), feedBlockCap)
}

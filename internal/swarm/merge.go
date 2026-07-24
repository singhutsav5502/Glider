package swarm

import (
	"fmt"
	"strings"

	"github.com/glider-ai/glider/internal/contextkit"
)

// Per-worker / merge retention for audit-sized fan-out text (hard caps).
const (
	mergeWorkerSummaryCap  = 16000
	mergeFailNoteCap       = 800
	orchestratorSummaryCap = 32000
)

// MergeResults is a callable weave stub: concatenates episode summaries into one
// Episode. Not full Slate thread-weaving — enough for FanOutExecutor text merge.
func MergeResults(results []Result) contextkit.Episode {
	var parts []string
	var tokens int
	var artifacts []string
	ok := 0
	role := ""
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		ok++
		label := r.WorkerID
		if label == "" {
			label = r.Model
		}
		sum := strings.TrimSpace(r.Episode.Summary)
		if sum == "" {
			sum = strings.TrimSpace(strings.Join(r.Episode.Artifacts, " "))
		}
		if sum != "" {
			parts = append(parts, fmt.Sprintf("[%s] %s", label, truncate(sum, mergeWorkerSummaryCap)))
		}
		tokens += r.Episode.Tokens
		artifacts = append(artifacts, r.Episode.Artifacts...)
		if role == "" && r.Role != "" {
			role = string(r.Role)
		}
		if role == "" {
			role = r.Episode.Role
		}
	}
	summary := strings.Join(parts, " | ")
	if summary == "" {
		summary = fmt.Sprintf("fan_out merged %d/%d workers", ok, len(results))
	}
	return contextkit.Episode{
		ID:        fmt.Sprintf("merge-%d", ok),
		Summary:   summary,
		Artifacts: artifacts,
		Tokens:    tokens,
		Model:     "glider-swarm",
		Reason:    "fan_out",
		Role:      role,
	}
}

// CritiqueMerge ranks successful workers, drops empty failures from the weave,
// and annotates fail count — a lightweight critic pass after fan-in (not an LLM).
func CritiqueMerge(results []Result) contextkit.Episode {
	type scored struct {
		r     Result
		score int
	}
	var okList []scored
	failN := 0
	var failNotes []string
	for _, r := range results {
		if r.Err != nil {
			failN++
			label := r.WorkerID
			if label == "" {
				label = string(r.Role)
			}
			failNotes = append(failNotes, fmt.Sprintf("%s:%s", label, truncate(r.Err.Error(), mergeFailNoteCap)))
			continue
		}
		sum := strings.TrimSpace(r.Episode.Summary)
		if sum == "" {
			sum = strings.TrimSpace(strings.Join(r.Episode.Artifacts, " "))
		}
		sc := len(sum) + r.Episode.Tokens*2
		if sc <= 0 {
			sc = 1
		}
		okList = append(okList, scored{r: r, score: sc})
	}
	// Prefer longer / higher-token summaries (weak quality proxy without an LLM critic).
	for i := 0; i < len(okList); i++ {
		for j := i + 1; j < len(okList); j++ {
			if okList[j].score > okList[i].score {
				okList[i], okList[j] = okList[j], okList[i]
			}
		}
	}
	ranked := make([]Result, len(okList))
	for i, s := range okList {
		ranked[i] = s.r
	}
	ep := MergeResults(ranked)
	ep.Reason = "critique_merge"
	ep.ID = fmt.Sprintf("critique-%d-%d", len(ranked), failN)
	prefix := fmt.Sprintf("critique ok=%d fail=%d", len(ranked), failN)
	if len(failNotes) > 0 {
		prefix += " drops=[" + strings.Join(failNotes, "; ") + "]"
	}
	if ep.Summary != "" {
		ep.Summary = prefix + " | " + ep.Summary
	} else {
		ep.Summary = prefix
	}
	return ep
}

// OrchestratorSummary is a compact string for dashboards / gateway finish text.
func OrchestratorSummary(merged contextkit.Episode, results []Result) string {
	ok, fail := 0, 0
	for _, r := range results {
		if r.Err != nil {
			fail++
		} else {
			ok++
		}
	}
	sum := strings.TrimSpace(merged.Summary)
	if sum == "" {
		sum = fmt.Sprintf("swarm %d ok / %d fail", ok, fail)
	}
	return fmt.Sprintf("swarm[%d/%d] %s", ok, ok+fail, truncate(sum, orchestratorSummaryCap))
}

// MergeTexts concatenates non-empty worker text blobs (gateway SSE merge helper).
func MergeTexts(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(fmt.Sprintf("--- worker %d ---\n%s", i+1, p))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

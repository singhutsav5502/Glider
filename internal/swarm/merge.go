package swarm

import (
	"fmt"
	"strings"

	"github.com/glider-ai/glider/internal/contextkit"
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
			parts = append(parts, fmt.Sprintf("[%s] %s", label, truncate(sum, 160)))
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

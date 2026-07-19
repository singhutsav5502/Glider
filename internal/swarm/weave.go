package swarm

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/glider-ai/glider/internal/contextkit"
)

// WeavePolicy selects how multi-wave episodes are combined.
type WeavePolicy string

const (
	WeaveConcatenate     WeavePolicy = "concatenate"
	WeaveRoleWeighted    WeavePolicy = "role_weighted"
	WeaveCritic          WeavePolicy = "critic"
	WeaveConflictCallout WeavePolicy = "conflict_callouts"
)

// NormalizeWeavePolicy maps aliases; empty → critic (P0 default path).
func NormalizeWeavePolicy(p WeavePolicy) WeavePolicy {
	switch WeavePolicy(strings.ToLower(strings.TrimSpace(string(p)))) {
	case WeaveConcatenate, "concat", "join":
		return WeaveConcatenate
	case WeaveRoleWeighted, "weighted", "roles":
		return WeaveRoleWeighted
	case WeaveConflictCallout, "conflict", "conflicts":
		return WeaveConflictCallout
	case WeaveCritic, "critique", "":
		return WeaveCritic
	default:
		return WeaveCritic
	}
}

// roleWeight prefers planner/critic narratives slightly over raw exec dumps.
func roleWeight(role string) float64 {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case string(RolePlan), "planner", "research":
		return 1.4
	case "critic", "review", "qa":
		return 1.3
	case string(RoleExec), "platform", "sre":
		return 1.1
	default:
		return 1.0
	}
}

// ApplyWeavePolicy combines wave merges + flat worker results per policy.
func ApplyWeavePolicy(policy WeavePolicy, waveMerges []contextkit.Episode, waveResults [][]Result) contextkit.Episode {
	policy = NormalizeWeavePolicy(policy)
	switch policy {
	case WeaveConcatenate:
		return weaveConcatenate(waveMerges)
	case WeaveRoleWeighted:
		return weaveRoleWeighted(waveMerges, waveResults)
	case WeaveConflictCallout:
		return weaveConflictCallouts(waveMerges, waveResults)
	default:
		return WeaveWaves(waveMerges, waveResults)
	}
}

func weaveConcatenate(waveMerges []contextkit.Episode) contextkit.Episode {
	var parts []string
	tokens := 0
	var arts []string
	for i, w := range waveMerges {
		sum := strings.TrimSpace(w.Summary)
		if sum == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[wave-%d] %s", i, truncate(sum, 280)))
		tokens += w.Tokens
		arts = append(arts, w.Artifacts...)
	}
	summary := strings.Join(parts, " || ")
	if summary == "" {
		summary = "weave: empty"
	}
	if len(summary) > 1200 {
		summary = summary[:1200] + "…"
	}
	return contextkit.Episode{
		ID:        fmt.Sprintf("weave-concat-%d", len(waveMerges)),
		Summary:   "weave[concatenate] " + summary,
		Artifacts: arts,
		Tokens:    tokens,
		Model:     "glider-swarm",
		Reason:    "weave",
		Role:      "weave",
	}
}

func weaveRoleWeighted(waveMerges []contextkit.Episode, waveResults [][]Result) contextkit.Episode {
	type scored struct {
		label string
		text  string
		w     float64
		tok   int
	}
	var items []scored
	for i, ep := range waveMerges {
		sum := strings.TrimSpace(ep.Summary)
		if sum == "" {
			continue
		}
		items = append(items, scored{
			label: fmt.Sprintf("wave-%d", i),
			text:  sum,
			w:     roleWeight(ep.Role) * float64(1+len(sum)/80),
			tok:   ep.Tokens,
		})
	}
	for wi, results := range waveResults {
		for _, r := range results {
			if r.Err != nil {
				continue
			}
			sum := strings.TrimSpace(r.Episode.Summary)
			if sum == "" {
				continue
			}
			items = append(items, scored{
				label: fmt.Sprintf("w%d:%s", wi, r.WorkerID),
				text:  sum,
				w:     roleWeight(string(r.Role)) * float64(1+len(sum)/100),
				tok:   r.Episode.Tokens,
			})
		}
	}
	// Descending weight.
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].w > items[i].w {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	var parts []string
	tokens := 0
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("[%.2f:%s] %s", it.w, it.label, truncate(it.text, 200)))
		tokens += it.tok
		if len(parts) >= 8 {
			break
		}
	}
	summary := strings.Join(parts, " || ")
	if summary == "" {
		summary = "weave: empty"
	}
	if len(summary) > 1200 {
		summary = summary[:1200] + "…"
	}
	return contextkit.Episode{
		ID:      fmt.Sprintf("weave-weighted-%d", len(items)),
		Summary: "weave[role_weighted] " + summary,
		Tokens:  tokens,
		Model:   "glider-swarm",
		Reason:  "weave",
		Role:    "weave",
	}
}

func weaveConflictCallouts(waveMerges []contextkit.Episode, waveResults [][]Result) contextkit.Episode {
	base := WeaveWaves(waveMerges, waveResults)
	var texts []string
	var labels []string
	for i, ep := range waveMerges {
		if s := strings.TrimSpace(ep.Summary); s != "" {
			texts = append(texts, s)
			labels = append(labels, fmt.Sprintf("wave-%d", i))
		}
	}
	for wi, results := range waveResults {
		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if s := strings.TrimSpace(r.Episode.Summary); s != "" {
				texts = append(texts, s)
				labels = append(labels, fmt.Sprintf("w%d:%s", wi, r.WorkerID))
			}
		}
	}
	conflicts := detectConflicts(labels, texts)
	prefix := "weave[conflict_callouts]"
	if len(conflicts) == 0 {
		prefix += " conflicts=0"
	} else {
		prefix += " conflicts=[" + strings.Join(conflicts, "; ") + "]"
	}
	sum := strings.TrimSpace(base.Summary)
	if sum == "" {
		sum = prefix
	} else {
		sum = prefix + " | " + sum
	}
	if len(sum) > 1400 {
		sum = sum[:1400] + "…"
	}
	base.Summary = sum
	base.Reason = "weave"
	base.ID = fmt.Sprintf("weave-conflict-%d", len(conflicts))
	return base
}

// detectConflicts flags pairs with opposing polarity cues or very low token overlap.
func detectConflicts(labels, texts []string) []string {
	if len(texts) < 2 {
		return nil
	}
	var out []string
	for i := 0; i < len(texts); i++ {
		for j := i + 1; j < len(texts); j++ {
			a, b := texts[i], texts[j]
			if polarityConflict(a, b) || (tokenOverlap(a, b) < 0.08 && len(a) > 40 && len(b) > 40) {
				out = append(out, fmt.Sprintf("%s<>%s", labels[i], labels[j]))
			}
			if len(out) >= 6 {
				return out
			}
		}
	}
	return out
}

func polarityConflict(a, b string) bool {
	al, bl := strings.ToLower(a), strings.ToLower(b)
	pos := []string{"go", "approve", "ship", "pass", "ready", "success", "lgtm"}
	neg := []string{"no-go", "block", "fail", "reject", "slip", "unsafe", "deny"}
	aPos, aNeg := hasAny(al, pos), hasAny(al, neg)
	bPos, bNeg := hasAny(bl, pos), hasAny(bl, neg)
	return (aPos && bNeg) || (aNeg && bPos)
}

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func tokenOverlap(a, b string) float64 {
	ta, tb := tokenSet(a), tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union <= 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	var b strings.Builder
	flush := func() {
		w := strings.ToLower(b.String())
		b.Reset()
		if len(w) < 3 {
			return
		}
		out[w] = true
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			flush()
		}
	}
	if b.Len() > 0 {
		flush()
	}
	return out
}

package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

const (
	defaultComplexityPriority  = 75
	defaultComplexityCloudAbove = 70
)

// ComplexityRule routes high-complexity turns to cloud/origin.
// Score source is routing.complexity_from (cursor|heuristic|both).
// Explicit /cloud|/local and sticky Path B belts still win — this only runs
// inside DecideLocal / Complete* Route evaluation.
type ComplexityRule struct {
	name     string
	priority int
	from     string // cursor | heuristic | both
	above    int
	action   config.ActionConfig
}

// NewComplexityRule builds the optional complexity→cloud rule, or nil when disabled.
func NewComplexityRule(routing config.RoutingConfig) Rule {
	cc := routing.Complexity
	if !cc.Enabled {
		return nil
	}
	from := strings.ToLower(strings.TrimSpace(routing.ComplexityFrom))
	if from == "" {
		from = "heuristic"
	}
	above := cc.CloudAbove
	if above <= 0 {
		above = defaultComplexityCloudAbove
	}
	pri := cc.Priority
	if pri == 0 {
		pri = defaultComplexityPriority
	}
	backendName := strings.TrimSpace(cc.CloudBackend)
	if backendName == "" {
		backendName = "openai"
	}
	model := strings.TrimSpace(cc.CloudModel)
	if model == "" {
		model = "gpt-4o"
	}
	return &ComplexityRule{
		name:     "Complexity→Cloud",
		priority: pri,
		from:     from,
		above:    above,
		action: config.ActionConfig{
			Target:  "cloud",
			Backend: backendName,
			Model:   model,
		},
	}
}

func (r *ComplexityRule) Name() string     { return r.name }
func (r *ComplexityRule) Priority() int    { return r.priority }

func (r *ComplexityRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	if r == nil || req == nil {
		return &RuleResult{Matched: false}, nil
	}
	score, source, ok := ResolveComplexity(req, r.from)
	if !ok {
		return &RuleResult{Matched: false}, nil
	}
	req.Metadata.ComplexityScore = score
	req.Metadata.ComplexitySource = source
	if score < r.above {
		return &RuleResult{Matched: false}, nil
	}
	return &RuleResult{
		Matched: true,
		Action: &backend.RoutingDecision{
			Strategy:    backend.StrategySingle,
			Target:      r.action.Target,
			BackendName: r.action.Backend,
			Model:       r.action.Model,
			Adapter:     r.action.Adapter,
			RuleName:    r.name,
			Reason:      "complexity_" + source,
			Role:        "plan",
		},
	}, nil
}

// ResolveComplexity returns (score, source, ok).
// ok=false when cursor-only mode has no Cursor field (fall through to other rules).
func ResolveComplexity(req *backend.CompletionRequest, from string) (score int, source string, ok bool) {
	from = strings.ToLower(strings.TrimSpace(from))
	if from == "" {
		from = "heuristic"
	}
	switch from {
	case "cursor":
		if req.Metadata.HasCursorComplexity {
			return clampScore(req.Metadata.CursorComplexity), "cursor", true
		}
		return 0, "", false
	case "both":
		if req.Metadata.HasCursorComplexity {
			return clampScore(req.Metadata.CursorComplexity), "cursor", true
		}
		return HeuristicComplexity(req), "heuristic", true
	default: // heuristic
		return HeuristicComplexity(req), "heuristic", true
	}
}

// HeuristicComplexity is the MVP score (0–100) from tools, file mentions,
// prompt length, and agent-mode strings. Prefer extracting a Cursor score when
// a wire field appears; do not call a model side-channel for this.
func HeuristicComplexity(req *backend.CompletionRequest) int {
	if req == nil {
		return 0
	}
	score := 0
	text := joinMessageText(req.Messages)

	if req.HasTools() {
		n := len(req.ToolNames())
		score += 35
		if n >= 8 {
			score += 15
		} else if n >= 3 {
			score += 8
		}
	}

	files := countFileMentions(text)
	switch {
	case files >= 6:
		score += 25
	case files >= 3:
		score += 15
	case files >= 1:
		score += 6
	}

	promptLen := len([]rune(text))
	switch {
	case promptLen >= 8000:
		score += 30
	case promptLen >= 2500:
		score += 18
	case promptLen >= 800:
		score += 8
	case promptLen >= 200:
		score += 3
	}

	score += modeStringBoost(text, req.Model)
	return clampScore(score)
}

var (
	// Paths like src/foo.go, pkg\bar.ts, D:\repos\x.go — used as a cheap file-count signal.
	filePathRE  = regexp.MustCompile(`(?i)(?:[A-Za-z]:)?(?:[\w.-]+[/\\])+[\w.-]+\.[A-Za-z0-9]{1,12}`)
	modeHeavyRE = regexp.MustCompile(`(?i)\b(architect(ure|ing)?|max\s*mode|agent\s*mode|plan\s*mode|thinking\s*(mode|level)|multi-?agent|swarm)\b`)
	modeLightRE = regexp.MustCompile(`(?i)\b(ask\s*mode|edit\s*mode|quick\s*(fix|question)|simple\s*(question|qa))\b`)
)

func countFileMentions(text string) int {
	matches := filePathRE.FindAllString(text, 64)
	seen := map[string]struct{}{}
	for _, m := range matches {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		seen[m] = struct{}{}
	}
	return len(seen)
}

func modeStringBoost(text, model string) int {
	blob := text + "\n" + model
	boost := 0
	if modeHeavyRE.MatchString(blob) {
		boost += 22
	}
	if modeLightRE.MatchString(blob) {
		boost -= 8
	}
	return boost
}

func joinMessageText(msgs []backend.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Content)
		if b.Len() > 200_000 {
			break
		}
	}
	return b.String()
}

func clampScore(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// TryAttachCursorComplexity is the plug-in for when Inspect/Extract discovers a
// Cursor complexity / tier / max_mode signal. Call from bidi_extract or decode
// once a stable wire tag is known. Safe no-op when score is absent.
func TryAttachCursorComplexity(req *backend.CompletionRequest, score int, present bool) {
	if req == nil || !present {
		return
	}
	req.Metadata.CursorComplexity = clampScore(score)
	req.Metadata.HasCursorComplexity = true
}

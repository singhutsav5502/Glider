package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

// handleRouterExplain serves POST /api/router/explain — the dry-run
// counterpart to the live routing decision every real completion goes
// through. It builds a hypothetical backend.CompletionRequest from the
// posted body and runs it through router.Engine.RouteExplain using an
// engine built from the operator's own live config, so the trace reflects
// their actual rule set, not a hardcoded example. Purely read-only: no
// completion is ever issued, and the engine used here is never the one
// serving real gateway/MITM traffic (built fresh, per request, from
// config alone). Script rules can't be constructed without a live
// StarlarkExecutor, which this dashboard-only code path deliberately
// doesn't have — see buildExplainEngine's doc comment for how that's
// surfaced instead of silently dropped or hard-failing the whole trace.
func (s *Server) handleRouterExplain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text             string   `json:"text"`
		Tools            []string `json:"tools,omitempty"`
		EstimatedTokens  int      `json:"estimatedTokens,omitempty"`
		CursorComplexity *int     `json:"cursorComplexity,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var cfg *config.Config
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	if cfg == nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}

	engine, skipped := buildExplainEngine(cfg.Routing)
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: body.Text}},
		Metadata: backend.RequestMetadata{EstimatedTokens: body.EstimatedTokens},
	}
	if len(body.Tools) > 0 {
		req.Tools = toolsJSON(body.Tools)
	}
	if body.CursorComplexity != nil {
		req.Metadata.HasCursorComplexity = true
		req.Metadata.CursorComplexity = *body.CursorComplexity
	}

	trace := engine.RouteExplain(r.Context(), req)
	writeJSON(w, explainResponseFromTrace(trace, skipped))
}

// handleRouterLint serves GET /api/router/lint — advisory, config-time-only
// ambiguity findings (router.LintConfig) over the operator's live routing
// rules. Companion to handleRouterExplain: a finding's Example field, when
// non-empty, is meant to be pasted straight into the explain box to watch
// the ambiguity actually play out for a concrete message.
func (s *Server) handleRouterLint(w http.ResponseWriter, r *http.Request) {
	var cfg *config.Config
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	if cfg == nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	findings := router.LintConfig(cfg.Routing)
	writeJSON(w, map[string]any{"findings": lintFindingsJSON(findings)})
}

// buildExplainEngine mirrors router.NewEngineFromConfig's construction
// order (configured rules, then injected task-classifier rules, then the
// optional complexity rule) but never fails the whole engine over one bad
// rule — a script rule can't be built without a live StarlarkExecutor
// (nil here, since the dashboard has no completion pipeline of its own to
// hang one off), and an operator's own regex/pattern typo should show up
// as "this one rule couldn't be evaluated," not as the entire Explain tool
// going dark. Skipped rules are reported back so the trace UI can say so
// plainly instead of just silently omitting them.
func buildExplainEngine(routing config.RoutingConfig) (*router.Engine, []string) {
	routing.ApplyDefaultTarget()
	var rules []router.Rule
	var skipped []string
	for _, ruleCfg := range routing.Rules {
		if !ruleCfg.IsEnabled() {
			continue
		}
		rule, err := router.NewRuleFromConfig(ruleCfg, nil)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %s", ruleCfg.Name, err.Error()))
			continue
		}
		rules = append(rules, rule)
	}
	if extra, err := router.NewTaskClassifierRules(routing.TaskClassifier, routing.ToolFollowup); err != nil {
		skipped = append(skipped, "task_classifier: "+err.Error())
	} else {
		rules = append(rules, extra...)
	}
	if cr := router.NewComplexityRule(routing); cr != nil {
		rules = append(rules, cr)
	}
	return router.NewEngine(rules), skipped
}

// toolsJSON builds the same JSON-array-of-{"name": ...} shape
// backend.CompletionRequest.Tools expects, so HasTools()/ToolNames() (and
// therefore the task classifier's tools→cloud rule) behave exactly as
// they would for a real request carrying an OpenAI/Anthropic tools array.
func toolsJSON(names []string) []byte {
	type tool struct {
		Name string `json:"name"`
	}
	tools := make([]tool, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		tools = append(tools, tool{Name: n})
	}
	if len(tools) == 0 {
		return nil
	}
	b, _ := json.Marshal(tools)
	return b
}

type explainDecisionJSON struct {
	Target      string `json:"target,omitempty"`
	BackendName string `json:"backendName,omitempty"`
	Model       string `json:"model,omitempty"`
	Adapter     string `json:"adapter,omitempty"`
	RuleName    string `json:"ruleName,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Role        string `json:"role,omitempty"`
}

type explainEntryJSON struct {
	RuleName string               `json:"ruleName"`
	Kind     string               `json:"kind"`
	Priority int                  `json:"priority"`
	Matched  bool                 `json:"matched"`
	Shadowed bool                 `json:"shadowed"`
	Decision *explainDecisionJSON `json:"decision,omitempty"`
	Err      string               `json:"err,omitempty"`
}

type explainResponse struct {
	Entries      []explainEntryJSON   `json:"entries"`
	Decision     *explainDecisionJSON `json:"decision,omitempty"`
	SkippedRules []string             `json:"skippedRules,omitempty"`
}

func decisionJSON(d *backend.RoutingDecision) *explainDecisionJSON {
	if d == nil {
		return nil
	}
	return &explainDecisionJSON{
		Target:      d.Target,
		BackendName: d.BackendName,
		Model:       d.Model,
		Adapter:     d.Adapter,
		RuleName:    d.RuleName,
		Reason:      d.Reason,
		Role:        d.Role,
	}
}

func explainResponseFromTrace(trace *router.RouteTrace, skipped []string) explainResponse {
	resp := explainResponse{SkippedRules: skipped}
	if trace == nil {
		return resp
	}
	resp.Decision = decisionJSON(trace.Decision)
	for _, e := range trace.Entries {
		resp.Entries = append(resp.Entries, explainEntryJSON{
			RuleName: e.RuleName,
			Kind:     e.Kind,
			Priority: e.Priority,
			Matched:  e.Matched,
			Shadowed: e.Shadowed,
			Decision: decisionJSON(e.Decision),
			Err:      e.Err,
		})
	}
	return resp
}

type lintFindingJSON struct {
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Rules    []string `json:"rules"`
	Example  string   `json:"example,omitempty"`
}

func lintFindingsJSON(findings []router.LintFinding) []lintFindingJSON {
	out := make([]lintFindingJSON, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintFindingJSON{
			Severity: f.Severity,
			Message:  f.Message,
			Rules:    f.Rules,
			Example:  f.Example,
		})
	}
	return out
}

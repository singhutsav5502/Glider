package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

// handleRouterExplain serves POST /api/router/explain. It is the dry-run
// equivalent of the live routing decision that each true completion uses.
//
// It makes a backend.CompletionRequest that is not true, from the body that a
// person posts. Then it sends that request through router.Engine.RouteExplain,
// with an engine that it makes from the live config of the operator. Therefore
// the trace shows the true set of rules of that operator, and not a fixed
// example.
//
// This code only reads. It issues no completion. The engine here is never the
// engine that serves the true gateway traffic or the true MITM traffic, because
// this code makes a new engine for each request, from the config alone.
//
// The code cannot make a script rule with no live StarlarkExecutor, and this
// code, which is for the dashboard only, does not have one. This is on purpose.
// Refer to the comment on buildExplainEngine for the method that shows this
// condition, instead of a silent omission or a hard failure of the full
// trace.
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

// handleRouterLint serves GET /api/router/lint. It gives findings about doubt
// in the config, from router.LintConfig, over the live routing rules of the
// operator. It gives advice, and it examines the config only.
//
// It operates with handleRouterExplain. When the Example field of a finding is
// not empty, a person copies it into the explain box. Then that person sees the
// doubt occur for a true message.
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

// buildExplainEngine uses the same sequence of construction as
// router.NewEngineFromConfig: the rules from the config, then the rules that
// the code adds for the task classifier, then the optional rule for
// complexity.
//
// But it never fails the full engine because of one incorrect rule. Two causes
// make this necessary. The code cannot make a script rule with no live
// StarlarkExecutor, and that value is nil here, because the dashboard has no
// completion pipeline of its own to give one. And an error by the operator in a
// regular expression or a pattern must appear as "the code could not evaluate
// this one rule". It must not make the full Explain tool stop.
//
// The code reports each rule that it did not use. Therefore the trace in the
// user interface can say this directly, and it does not omit those rules with
// no message.
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

// toolsJSON makes the same shape that backend.CompletionRequest.Tools needs,
// which is a JSON array of {"name": ...}. Therefore HasTools() and ToolNames()
// operate exactly as they operate for a true request with an array of tools
// from OpenAI or from Anthropic. The rule of the task classifier that sends a
// request with tools to the cloud also operates in the same way.
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

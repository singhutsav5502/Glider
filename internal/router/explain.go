package router

import (
	"context"
	"sort"

	"github.com/glider-ai/glider/internal/backend"
)

// RouteTraceEntry is one rule's outcome in a RouteExplain trace.
type RouteTraceEntry struct {
	RuleName string
	Kind     string // human label for the rule's Go type — see ruleKind
	Priority int
	Matched  bool
	// Shadowed is true when this rule matched but a higher-priority (or,
	// for tied explicit commands, earlier-in-priority-order) rule already
	// won — exactly the ambiguity LintConfig's duplicate-trigger findings
	// point at in the abstract. Seeing it marked here, for a real message,
	// is what turns "these two rules might conflict" into "here's what
	// actually happens."
	Shadowed bool
	Decision *backend.RoutingDecision // set only when Matched
	Err      string                   // set if Evaluate itself returned an error
}

// RouteTrace is the full result of RouteExplain: every rule Route would
// have considered for req, in the same order Route itself uses, and the
// same final decision Route would return.
type RouteTrace struct {
	Entries  []RouteTraceEntry
	Decision *backend.RoutingDecision // nil if nothing matched (mirrors Route's "no routing rule matched")
}

func (t *RouteTrace) record(rule Rule, priority int, kind string, result *RuleResult, err error) {
	entry := RouteTraceEntry{RuleName: rule.Name(), Kind: kind, Priority: priority}
	switch {
	case err != nil:
		entry.Err = err.Error()
	case result != nil && result.Matched && result.Action != nil:
		entry.Matched = true
		action := result.Action
		if action.Strategy == "" {
			action.Strategy = backend.StrategySingle
		}
		if action.RuleName == "" {
			action.RuleName = rule.Name()
		}
		entry.Decision = action
		if t.Decision == nil {
			t.Decision = action
		} else {
			entry.Shadowed = true
		}
	}
	t.Entries = append(t.Entries, entry)
}

// RouteExplain runs the exact same evaluation Route does — same rules,
// same explicit-command-first ordering, same priority-sorted remainder —
// but where Route returns on the first match, RouteExplain keeps going
// through every remaining rule so the trace shows the whole candidate
// list: what would also have matched if a higher-priority rule hadn't
// already won. Purely a read-only reflection over the live rule set for a
// hypothetical request — never called from the real completion path, so
// it carries zero risk to how Route itself behaves.
//
// Each rule is evaluated against its own clone of req, not a request
// shared across all of them: ExplicitCommandRule.Evaluate mutates the
// matched message's Content in place (stripping the matched command so
// downstream backends never see literal "/cloud" text) as a real, load-
// bearing side effect on the live Route path, where only ever one
// explicit rule actually runs per request. RouteExplain deliberately runs
// every explicit rule for visibility, so without cloning, whichever rule
// evaluates first would silently strip the command before the next rule
// with the same trigger ever got to look — turning "these two rules both
// match" into a false "only the first one matches," exactly the ambiguity
// this trace exists to surface. Caught by
// TestRouteExplain_MarksLoserAsShadowed failing against the uncloned
// version before this fix.
func (e *Engine) RouteExplain(ctx context.Context, req *backend.CompletionRequest) *RouteTrace {
	trace := &RouteTrace{}
	if e == nil || req == nil {
		return trace
	}

	var explicits []*ExplicitCommandRule
	for _, rule := range e.rules {
		if er, ok := rule.(*ExplicitCommandRule); ok {
			explicits = append(explicits, er)
		}
	}
	sort.SliceStable(explicits, func(i, j int) bool { return explicits[i].Priority() > explicits[j].Priority() })
	for _, er := range explicits {
		result, err := er.Evaluate(ctx, cloneCompletionRequest(req))
		trace.record(er, er.Priority(), "explicit", result, err)
	}

	for _, rule := range e.rules {
		if _, ok := rule.(*ExplicitCommandRule); ok {
			continue // already handled above, same skip Route itself applies
		}
		result, err := rule.Evaluate(ctx, cloneCompletionRequest(req))
		trace.record(rule, rule.Priority(), ruleKind(rule), result, err)
	}
	return trace
}

// cloneCompletionRequest gives each rule evaluation in RouteExplain an
// independent copy — a shallow struct copy plus a fresh backing array for
// Messages (each Message is copied by value; ToolCalls' underlying bytes
// are shared but never mutated by any rule, so sharing them is safe).
func cloneCompletionRequest(req *backend.CompletionRequest) *backend.CompletionRequest {
	clone := *req
	clone.Messages = append([]backend.Message(nil), req.Messages...)
	return &clone
}

func ruleKind(rule Rule) string {
	switch rule.(type) {
	case *ExplicitCommandRule:
		return "explicit"
	case *RegexRule:
		return "regex"
	case *ContextSizeRule:
		return "context_size"
	case *AlwaysRule:
		return "always"
	case *StarlarkScriptRule:
		return "script"
	case *ComposerWrapupOriginRule:
		return "composer_wrapup"
	case *TaskClassRule:
		return "task_classifier"
	case *ComplexityRule:
		return "complexity"
	default:
		return "unknown"
	}
}

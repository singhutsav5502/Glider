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
	// Shadowed is true when this rule agrees with the request, but a different
	// rule already won. That rule has a higher priority. For two explicit commands
	// with the same priority, that rule is earlier in the sequence.
	//
	// This is exactly the doubt that the findings of LintConfig about a repeated
	// trigger describe in a general way. To see the mark here, for a true message,
	// changes "these two rules can disagree" into "this is what truly
	// occurs".
	Shadowed bool
	Decision *backend.RoutingDecision // set only when Matched
	Err      string                   // set if Evaluate itself returned an error
}

// RouteTrace is the full result of RouteExplain. It has each rule that Route
// would examine for req, in the same sequence that Route uses, and the same
// final decision that Route would return.
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

// RouteExplain does exactly the same evaluation as Route. It uses the same
// rules, the same sequence that puts an explicit command first, and the same
// remainder in the sequence of priority.
//
// But Route returns at the first rule that agrees. RouteExplain continues
// through each remaining rule. Therefore the trace shows the full list of
// candidates: which other rules would agree, if a rule with a higher priority
// had not won first.
//
// This is a reflection over the live set of rules for a request that is not
// true, and it only reads. No code calls it on the true completion path.
// Therefore it has no risk for the behaviour of Route.
//
// The code evaluates each rule against its own copy of req, and not against one
// request that each rule shares.
//
// The cause: ExplicitCommandRule.Evaluate changes the Content of the message
// that agrees, in its position. It removes the command that agrees, thus a
// backend later never sees the text "/cloud". That change is true and
// necessary on the live path of Route, where exactly one explicit rule
// operates for each request.
//
// RouteExplain runs each explicit rule, thus a person can see them all. With no
// copy, the rule that evaluates first would remove the command before the next
// rule with the same trigger could examine it. That changes "these two rules
// both agree" into an incorrect "only the first one agrees", and that is
// exactly the doubt that this trace must show.
//
// TestRouteExplain_MarksLoserAsShadowed found this. It failed against the
// version with no copy, before this correction.
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

// cloneCompletionRequest gives an independent copy to each evaluation of a
// rule in RouteExplain.
//
// It copies the structure at the first level, and it makes a new array for
// Messages. It copies each Message by value. The bytes under ToolCalls stay
// shared, but no rule changes them, therefore it is safe to share them.
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

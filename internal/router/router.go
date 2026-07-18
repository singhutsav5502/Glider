package router

import (
	"context"
	"fmt"
	"sort"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

// Router evaluates rules and returns a routing decision.
type Router interface {
	Route(ctx context.Context, req *backend.CompletionRequest) (*backend.RoutingDecision, error)
}

// Engine evaluates configured rules in priority order.
type Engine struct {
	rules []Rule
}

// NewEngine creates a router engine from an ordered list of rules.
func NewEngine(rules []Rule) *Engine {
	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority() > sorted[j].Priority()
	})
	return &Engine{rules: sorted}
}

// NewEngineFromConfig builds an engine from routing configuration.
// When routing.task_classifier.enabled, injects heuristic rules (tools / must-cloud /
// small-local) so task shape beats token thresholds; explicit /local|/cloud still win
// when their priorities are higher (default 100/99).
// When routing.complexity.enabled, injects Complexity→Cloud (see complexity_from).
func NewEngineFromConfig(routing config.RoutingConfig, executor *StarlarkExecutor) (*Engine, error) {
	routing.ApplyDefaultTarget()
	rules := make([]Rule, 0, len(routing.Rules)+4)
	for _, cfg := range routing.Rules {
		if !cfg.IsEnabled() {
			continue
		}
		rule, err := NewRuleFromConfig(cfg, executor)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	extra, err := NewTaskClassifierRules(routing.TaskClassifier, routing.ToolFollowup)
	if err != nil {
		return nil, err
	}
	rules = append(rules, extra...)
	if cr := NewComplexityRule(routing); cr != nil {
		rules = append(rules, cr)
	}
	return NewEngine(rules), nil
}

// Route evaluates rules in descending priority order and returns the first match.
// Explicit /local|/cloud|/fast|/heavy always win over task_classifier and thresholds,
// even if a misconfigured priority would otherwise invert them.
func (e *Engine) Route(ctx context.Context, req *backend.CompletionRequest) (*backend.RoutingDecision, error) {
	if d := e.explicitHardOverride(ctx, req); d != nil {
		return d, nil
	}
	for _, rule := range e.rules {
		// Explicit rules already considered above.
		if _, ok := rule.(*ExplicitCommandRule); ok {
			continue
		}
		result, err := rule.Evaluate(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.Name(), err)
		}
		if result.Matched && result.Action != nil {
			if result.Action.Strategy == "" {
				result.Action.Strategy = backend.StrategySingle
			}
			return result.Action, nil
		}
	}
	return nil, fmt.Errorf("no routing rule matched")
}

// explicitHardOverride forces configured ExplicitCommandRule matches before any
// classifier / script / token rule, sorted by rule priority (desc).
func (e *Engine) explicitHardOverride(ctx context.Context, req *backend.CompletionRequest) *backend.RoutingDecision {
	if e == nil || req == nil {
		return nil
	}
	var explicits []*ExplicitCommandRule
	for _, rule := range e.rules {
		if er, ok := rule.(*ExplicitCommandRule); ok {
			explicits = append(explicits, er)
		}
	}
	if len(explicits) == 0 {
		return nil
	}
	sort.SliceStable(explicits, func(i, j int) bool {
		return explicits[i].Priority() > explicits[j].Priority()
	})
	for _, er := range explicits {
		result, err := er.Evaluate(ctx, req)
		if err != nil || result == nil || !result.Matched || result.Action == nil {
			continue
		}
		if result.Action.Strategy == "" {
			result.Action.Strategy = backend.StrategySingle
		}
		if result.Action.Reason == "" {
			result.Action.Reason = "explicit_override"
		}
		return result.Action
	}
	return nil
}

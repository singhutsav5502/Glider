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
func NewEngineFromConfig(routing config.RoutingConfig, executor *StarlarkExecutor) (*Engine, error) {
	rules := make([]Rule, 0, len(routing.Rules))
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
	return NewEngine(rules), nil
}

// Route evaluates rules in descending priority order and returns the first match.
func (e *Engine) Route(ctx context.Context, req *backend.CompletionRequest) (*backend.RoutingDecision, error) {
	for _, rule := range e.rules {
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

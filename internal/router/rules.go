package router

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

// Rule evaluates a single routing rule against a completion request.
type Rule interface {
	Name() string
	Priority() int
	Evaluate(ctx context.Context, req *backend.CompletionRequest) (*RuleResult, error)
}

// RuleResult is the outcome of evaluating one rule.
type RuleResult struct {
	Matched bool
	Action  *backend.RoutingDecision
}

// ExplicitCommandRule matches message prefixes like "/local".
type ExplicitCommandRule struct {
	name     string
	priority int
	commands []string
	action   config.ActionConfig
}

func NewExplicitCommandRule(cfg config.RuleConfig) (*ExplicitCommandRule, error) {
	if len(cfg.Trigger.Commands) == 0 {
		return nil, fmt.Errorf("explicit rule %q: commands required", cfg.Name)
	}
	return &ExplicitCommandRule{
		name:     cfg.Name,
		priority: cfg.Priority,
		commands: cfg.Trigger.Commands,
		action:   cfg.Action,
	}, nil
}

func (r *ExplicitCommandRule) Name() string     { return r.name }
func (r *ExplicitCommandRule) Priority() int    { return r.priority }

func (r *ExplicitCommandRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	if req == nil || len(req.Messages) == 0 {
		return &RuleResult{Matched: false}, nil
	}
	// Prefer last user message (TipTap extract is usually a single user turn).
	// Fall back to scanning all messages so chrome/system text cannot bury /cloud.
	indices := make([]int, 0, len(req.Messages))
	for i := len(req.Messages) - 1; i >= 0; i-- {
		indices = append(indices, i)
	}
	for _, idx := range indices {
		cmd, remainder, ok := MatchExplicitCommand(req.Messages[idx].Content, r.commands)
		if !ok {
			continue
		}
		_ = cmd
		req.Messages[idx].Content = remainder
		return &RuleResult{
			Matched: true,
			Action:  actionToDecision(r.name, r.action),
		}, nil
	}
	return &RuleResult{Matched: false}, nil
}

// RegexRule matches the last message against a regular expression.
type RegexRule struct {
	name     string
	priority int
	pattern  *regexp.Regexp
	action   config.ActionConfig
}

func NewRegexRule(cfg config.RuleConfig) (*RegexRule, error) {
	if cfg.Trigger.Pattern == "" {
		return nil, fmt.Errorf("regex rule %q: pattern required", cfg.Name)
	}
	re, err := regexp.Compile(cfg.Trigger.Pattern)
	if err != nil {
		return nil, fmt.Errorf("regex rule %q: %w", cfg.Name, err)
	}
	return &RegexRule{
		name:     cfg.Name,
		priority: cfg.Priority,
		pattern:  re,
		action:   cfg.Action,
	}, nil
}

func (r *RegexRule) Name() string     { return r.name }
func (r *RegexRule) Priority() int    { return r.priority }

func (r *RegexRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	content := lastMessageContent(req)
	if r.pattern.MatchString(content) {
		return &RuleResult{
			Matched: true,
			Action:  actionToDecision(r.name, r.action),
		}, nil
	}
	return &RuleResult{Matched: false}, nil
}

// ContextSizeRule matches on estimated token count (token ceiling; task_classifier sits above).
type ContextSizeRule struct {
	name     string
	priority int
	operator string
	value    int
	action   config.ActionConfig
}

func NewContextSizeRule(cfg config.RuleConfig) (*ContextSizeRule, error) {
	if cfg.Trigger.Operator == "" {
		return nil, fmt.Errorf("context_size rule %q: operator required", cfg.Name)
	}
	return &ContextSizeRule{
		name:     cfg.Name,
		priority: cfg.Priority,
		operator: cfg.Trigger.Operator,
		value:    cfg.Trigger.Value,
		action:   cfg.Action,
	}, nil
}

func (r *ContextSizeRule) Name() string     { return r.name }
func (r *ContextSizeRule) Priority() int    { return r.priority }

func (r *ContextSizeRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	tokens := req.Metadata.EstimatedTokens
	if compareTokens(tokens, r.operator, r.value) {
		return &RuleResult{
			Matched: true,
			Action:  actionToDecision(r.name, r.action),
		}, nil
	}
	return &RuleResult{Matched: false}, nil
}

// AlwaysRule always matches (typically used as a default fallback).
type AlwaysRule struct {
	name     string
	priority int
	action   config.ActionConfig
}

func NewAlwaysRule(cfg config.RuleConfig) *AlwaysRule {
	return &AlwaysRule{
		name:     cfg.Name,
		priority: cfg.Priority,
		action:   cfg.Action,
	}
}

func (r *AlwaysRule) Name() string     { return r.name }
func (r *AlwaysRule) Priority() int    { return r.priority }

func (r *AlwaysRule) Evaluate(_ context.Context, _ *backend.CompletionRequest) (*RuleResult, error) {
	return &RuleResult{
		Matched: true,
		Action:  actionToDecision(r.name, r.action),
	}, nil
}

// StarlarkScriptRule delegates evaluation to a Starlark script.
type StarlarkScriptRule struct {
	name     string
	priority int
	script   string
	action   config.ActionConfig
	executor *StarlarkExecutor
}

func NewStarlarkScriptRule(cfg config.RuleConfig, executor *StarlarkExecutor) (*StarlarkScriptRule, error) {
	if cfg.Trigger.File == "" {
		return nil, fmt.Errorf("script rule %q: file required", cfg.Name)
	}
	return &StarlarkScriptRule{
		name:     cfg.Name,
		priority: cfg.Priority,
		script:   cfg.Trigger.File,
		action:   cfg.Action,
		executor: executor,
	}, nil
}

func (r *StarlarkScriptRule) Name() string     { return r.name }
func (r *StarlarkScriptRule) Priority() int    { return r.priority }

func (r *StarlarkScriptRule) Evaluate(ctx context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	result, err := r.executor.Run(ctx, r.script, req)
	if err != nil {
		return nil, err
	}
	if !result.Matched {
		return &RuleResult{Matched: false}, nil
	}
	decision := result.Action
	if decision == nil {
		decision = actionToDecision(r.name, r.action)
	} else {
		decision.RuleName = r.name
		if decision.Strategy == "" {
			decision.Strategy = backend.StrategySingle
		}
	}
	return &RuleResult{Matched: true, Action: decision}, nil
}

// NewRuleFromConfig constructs a Rule from configuration.
func NewRuleFromConfig(cfg config.RuleConfig, executor *StarlarkExecutor) (Rule, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Trigger.Type)) {
	case "explicit":
		return NewExplicitCommandRule(cfg)
	case "regex":
		return NewRegexRule(cfg)
	case "context_size":
		return NewContextSizeRule(cfg)
	case "always":
		return NewAlwaysRule(cfg), nil
	case "script":
		if executor == nil {
			return nil, fmt.Errorf("script rule %q: starlark executor required", cfg.Name)
		}
		return NewStarlarkScriptRule(cfg, executor)
	case TriggerComposerWrapup, "wrapup_origin", "composer_wrapup_origin":
		return NewComposerWrapupOriginRule(cfg), nil
	default:
		return nil, fmt.Errorf("unknown trigger type %q for rule %q", cfg.Trigger.Type, cfg.Name)
	}
}

func actionToDecision(ruleName string, action config.ActionConfig) *backend.RoutingDecision {
	return &backend.RoutingDecision{
		Strategy:    backend.StrategySingle,
		Target:      action.Target,
		BackendName: action.Backend,
		Model:       action.Model,
		Adapter:     action.Adapter,
		RuleName:    ruleName,
	}
}

func lastMessageContent(req *backend.CompletionRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Content
}

func compareTokens(tokens int, operator string, value int) bool {
	switch operator {
	case ">":
		return tokens > value
	case ">=":
		return tokens >= value
	case "<":
		return tokens < value
	case "<=":
		return tokens <= value
	case "==", "=":
		return tokens == value
	default:
		return false
	}
}

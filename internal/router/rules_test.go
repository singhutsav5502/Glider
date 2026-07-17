package router_test

import (
	"context"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func testCtx() context.Context {
	return context.Background()
}

func explicitRuleConfig() config.RuleConfig {
	return config.RuleConfig{
		Name:     "Explicit Local",
		Priority: 100,
		Trigger: config.TriggerConfig{
			Type:     "explicit",
			Commands: []string{"/local", "/fast"},
		},
		Action: config.ActionConfig{
			Target: "local",
			Model:  "codellama:7b",
		},
	}
}

func regexRuleConfig() config.RuleConfig {
	return config.RuleConfig{
		Name:     "Refactor Detector",
		Priority: 50,
		Trigger: config.TriggerConfig{
			Type:    "regex",
			Pattern: `(?i)\b(refactor|rename|extract)\b`,
		},
		Action: config.ActionConfig{
			Target: "local",
			Model:  "codellama:7b",
		},
	}
}

func contextSizeRuleConfig() config.RuleConfig {
	return config.RuleConfig{
		Name:     "Context Overflow",
		Priority: 10,
		Trigger: config.TriggerConfig{
			Type:     "context_size",
			Operator: ">",
			Value:    8000,
		},
		Action: config.ActionConfig{
			Target:  "cloud",
			Backend: "openai",
			Model:   "gpt-4o",
		},
	}
}

func alwaysRuleConfig() config.RuleConfig {
	return config.RuleConfig{
		Name:     "Default Local",
		Priority: 0,
		Trigger:  config.TriggerConfig{Type: "always"},
		Action: config.ActionConfig{
			Target: "local",
			Model:  "codellama:7b",
		},
	}
}

// T2.4.1 — ExplicitCommandRule: match "/local" prefix
func TestT2_4_1_ExplicitCommandRule_Match(t *testing.T) {
	rule, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "/local refactor this function"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action == nil {
		t.Fatal("expected action")
	}
	if result.Action.Target != "local" {
		t.Fatalf("target = %q, want local", result.Action.Target)
	}
	if result.Action.Model != "codellama:7b" {
		t.Fatalf("model = %q, want codellama:7b", result.Action.Model)
	}
}

// T2.4.2 — ExplicitCommandRule: no match
func TestT2_4_2_ExplicitCommandRule_NoMatch(t *testing.T) {
	rule, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "refactor this function"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched {
		t.Fatal("expected no match")
	}
}

// T2.4.3 — ExplicitCommandRule: strip command from message before forwarding
func TestT2_4_3_ExplicitCommandRule_StripPrefix(t *testing.T) {
	rule, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "/local refactor this function"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
	if req.Messages[len(req.Messages)-1].Content != "refactor this function" {
		t.Fatalf("content = %q, want %q", req.Messages[len(req.Messages)-1].Content, "refactor this function")
	}
}

// T2.4.4 — RegexRule: match pattern
func TestT2_4_4_RegexRule_Match(t *testing.T) {
	rule, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Please refactor this class"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
}

// T2.4.5 — RegexRule: no match
func TestT2_4_5_RegexRule_NoMatch(t *testing.T) {
	rule, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Explain how this works"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched {
		t.Fatal("expected no match")
	}
}

// T2.4.6 — ContextSizeRule: over threshold
func TestT2_4_6_ContextSizeRule_OverThreshold(t *testing.T) {
	rule, err := router.NewContextSizeRule(contextSizeRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Metadata: backend.RequestMetadata{EstimatedTokens: 12000},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
}

// T2.4.7 — ContextSizeRule: under threshold
func TestT2_4_7_ContextSizeRule_UnderThreshold(t *testing.T) {
	rule, err := router.NewContextSizeRule(contextSizeRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Metadata: backend.RequestMetadata{EstimatedTokens: 4000},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched {
		t.Fatal("expected no match")
	}
}

// T2.4.8 — Router: first matching rule wins (priority ordering)
func TestT2_4_8_Router_PriorityOrdering(t *testing.T) {
	explicit, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	regex, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	contextSize, err := router.NewContextSizeRule(contextSizeRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	engine := router.NewEngine([]router.Rule{explicit, regex, contextSize})

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Please refactor this class"},
		},
		Metadata: backend.RequestMetadata{EstimatedTokens: 12000},
	}

	decision, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.RuleName != "Refactor Detector" {
		t.Fatalf("RuleName = %q, want Refactor Detector", decision.RuleName)
	}
	if decision.Target != "local" {
		t.Fatalf("target = %q, want local", decision.Target)
	}
}

// T2.4.9 — Router: no rule matches → default rule
func TestT2_4_9_Router_DefaultRule(t *testing.T) {
	explicit, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	regex, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	contextSize, err := router.NewContextSizeRule(contextSizeRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defaultRule := router.NewAlwaysRule(alwaysRuleConfig())

	engine := router.NewEngine([]router.Rule{explicit, regex, contextSize, defaultRule})

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Explain how this works"},
		},
		Metadata: backend.RequestMetadata{EstimatedTokens: 4000},
	}

	decision, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.RuleName != "Default Local" {
		t.Fatalf("RuleName = %q, want Default Local", decision.RuleName)
	}
	if decision.Target != "local" {
		t.Fatalf("target = %q, want local", decision.Target)
	}
}

// T2.4.10 — RoutingDecision defaults to Strategy = "single"
func TestT2_4_10_RoutingDecision_DefaultStrategy(t *testing.T) {
	rule, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "/local refactor this function"},
		},
	}

	result, err := rule.Evaluate(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Strategy != backend.StrategySingle {
		t.Fatalf("strategy = %q, want single", result.Action.Strategy)
	}
}

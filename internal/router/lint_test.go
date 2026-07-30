package router_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestLintConfig_DuplicateExplicitCommand(t *testing.T) {
	routing := config.RoutingConfig{Rules: []config.RuleConfig{
		explicitCommandConfig("Force Cloud A", 100, "/cloud"),
		explicitCommandConfig("Force Cloud B", 50, "/cloud"),
	}}
	findings := router.LintConfig(routing)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Rules[0] != "Force Cloud A" {
		t.Fatalf("winner should be listed first (higher priority), got %+v", f.Rules)
	}
	if f.Example != "/cloud example message" {
		t.Fatalf("got example %q, want a synthesized /cloud message", f.Example)
	}
}

func TestLintConfig_DisabledRuleExcluded(t *testing.T) {
	disabledFalse := false
	routing := config.RoutingConfig{Rules: []config.RuleConfig{
		explicitCommandConfig("Force Cloud A", 100, "/cloud"),
		{
			Name:     "Force Cloud B",
			Priority: 50,
			Enabled:  &disabledFalse,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud"},
		},
	}}
	findings := router.LintConfig(routing)
	if len(findings) != 0 {
		t.Fatalf("a disabled rule should never be part of a collision, got %+v", findings)
	}
}

func TestLintConfig_DuplicateRegexTriggerShape(t *testing.T) {
	routing := config.RoutingConfig{Rules: []config.RuleConfig{
		{
			Name:     "Refactor A",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`},
			Action:   config.ActionConfig{Target: "local"},
		},
		{
			Name:     "Refactor B (dead)",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`},
			Action:   config.ActionConfig{Target: "cloud"},
		},
	}}
	findings := router.LintConfig(routing)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Example != "" {
		t.Fatalf("regex collisions should not fabricate an example, got %q", findings[0].Example)
	}
	if len(findings[0].Rules) != 2 || findings[0].Rules[0] != "Refactor A" {
		t.Fatalf("got rules %+v", findings[0].Rules)
	}
}

func TestLintConfig_DifferentPriority_NoFinding(t *testing.T) {
	routing := config.RoutingConfig{Rules: []config.RuleConfig{
		{
			Name:     "Refactor A",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`},
			Action:   config.ActionConfig{Target: "local"},
		},
		{
			Name:     "Refactor B",
			Priority: 40,
			Trigger:  config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`},
			Action:   config.ActionConfig{Target: "cloud"},
		},
	}}
	// Different priorities are a deliberate, reachable fallback chain, not
	// an ambiguity — LintConfig must not flag it.
	if findings := router.LintConfig(routing); len(findings) != 0 {
		t.Fatalf("expected no findings for differently-prioritized rules, got %+v", findings)
	}
}

func TestLintConfig_CleanConfig_NoFindings(t *testing.T) {
	routing := config.RoutingConfig{Rules: []config.RuleConfig{
		explicitRuleConfig(),
		regexRuleConfig(),
		contextSizeRuleConfig(),
		alwaysRuleConfig(),
	}}
	if findings := router.LintConfig(routing); len(findings) != 0 {
		t.Fatalf("expected no findings for a normal, non-colliding config, got %+v", findings)
	}
}

package router_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestRouteExplain_MatchesRouteDecision(t *testing.T) {
	explicit, err := router.NewExplicitCommandRule(explicitRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	regex, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	always := router.NewAlwaysRule(alwaysRuleConfig())
	engine := router.NewEngine([]router.Rule{explicit, regex, always})

	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "Please refactor this class"}},
	}

	decision, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}

	trace := engine.RouteExplain(testCtx(), req)
	if trace.Decision == nil || trace.Decision.RuleName != decision.RuleName {
		t.Fatalf("RouteExplain decision = %+v, want it to match Route's own decision %+v", trace.Decision, decision)
	}
	// Route only ever sees the winner; RouteExplain must show every rule
	// that was actually evaluated, including ones that never had a
	// chance to win.
	if len(trace.Entries) != 3 {
		t.Fatalf("got %d trace entries, want 3 (one per configured rule): %+v", len(trace.Entries), trace.Entries)
	}
}

func TestRouteExplain_MarksLoserAsShadowed(t *testing.T) {
	// Two explicit rules that both claim "/cloud" — same scenario
	// lintDuplicateExplicitCommands flags; RouteExplain should show the
	// higher-priority one winning and the other one shadowed, not silently
	// dropped from the trace.
	winner, err := router.NewExplicitCommandRule(explicitCommandConfig("Force Cloud A", 100, "/cloud"))
	if err != nil {
		t.Fatal(err)
	}
	loser, err := router.NewExplicitCommandRule(explicitCommandConfig("Force Cloud B", 50, "/cloud"))
	if err != nil {
		t.Fatal(err)
	}
	engine := router.NewEngine([]router.Rule{winner, loser})

	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "do the thing /cloud"}},
	}

	trace := engine.RouteExplain(testCtx(), req)
	if trace.Decision == nil || trace.Decision.RuleName != "Force Cloud A" {
		t.Fatalf("decision = %+v, want Force Cloud A to win (higher priority)", trace.Decision)
	}

	var sawWinnerMatched, sawLoserShadowed bool
	for _, e := range trace.Entries {
		if e.RuleName == "Force Cloud A" && e.Matched && !e.Shadowed {
			sawWinnerMatched = true
		}
		if e.RuleName == "Force Cloud B" && e.Matched && e.Shadowed {
			sawLoserShadowed = true
		}
	}
	if !sawWinnerMatched {
		t.Fatalf("expected Force Cloud A entry matched, not shadowed: %+v", trace.Entries)
	}
	if !sawLoserShadowed {
		t.Fatalf("expected Force Cloud B entry matched AND shadowed, got: %+v", trace.Entries)
	}
}

func TestRouteExplain_NoMatch_DecisionNil(t *testing.T) {
	regex, err := router.NewRegexRule(regexRuleConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine := router.NewEngine([]router.Rule{regex})

	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "nothing matches this"}},
	}
	trace := engine.RouteExplain(testCtx(), req)
	if trace.Decision != nil {
		t.Fatalf("expected nil decision, got %+v", trace.Decision)
	}
	if len(trace.Entries) != 1 || trace.Entries[0].Matched {
		t.Fatalf("expected one unmatched entry, got %+v", trace.Entries)
	}
}

func explicitCommandConfig(name string, priority int, command string) config.RuleConfig {
	return config.RuleConfig{
		Name:     name,
		Priority: priority,
		Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{command}},
		Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
	}
}

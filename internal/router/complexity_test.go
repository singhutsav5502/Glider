package router_test

import (
	"context"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestHeuristicComplexity_ToolsAndFiles(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Please update src/a.go and pkg/b/c.ts and internal/x.go"},
		},
		Tools: []byte(`[{"type":"function","function":{"name":"read_file"}},{"type":"function","function":{"name":"grep"}},{"type":"function","function":{"name":"Shell"}}]`),
	}
	score := router.HeuristicComplexity(req)
	if score < 50 {
		t.Fatalf("expected elevated score with tools+files, got %d", score)
	}
}

func TestHeuristicComplexity_ModeStrings(t *testing.T) {
	light := router.HeuristicComplexity(&backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "ask mode: what is a mutex?"}},
	})
	heavy := router.HeuristicComplexity(&backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "architect the module boundaries in agent mode"}},
	})
	if heavy <= light {
		t.Fatalf("heavy mode score %d should exceed light %d", heavy, light)
	}
}

func TestResolveComplexity_CursorBothHeuristic(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	router.TryAttachCursorComplexity(req, 88, true)

	score, src, ok := router.ResolveComplexity(req, "cursor")
	if !ok || score != 88 || src != "cursor" {
		t.Fatalf("cursor: got %d %q ok=%v", score, src, ok)
	}
	score, src, ok = router.ResolveComplexity(req, "both")
	if !ok || score != 88 || src != "cursor" {
		t.Fatalf("both with cursor: got %d %q ok=%v", score, src, ok)
	}

	req2 := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	_, _, ok = router.ResolveComplexity(req2, "cursor")
	if ok {
		t.Fatal("cursor-only without field should not match")
	}
	score, src, ok = router.ResolveComplexity(req2, "both")
	if !ok || src != "heuristic" {
		t.Fatalf("both without cursor: got %d %q ok=%v", score, src, ok)
	}
}

func TestComplexityRule_RoutesCloudAbove(t *testing.T) {
	rule := router.NewComplexityRule(config.RoutingConfig{
		ComplexityFrom: "heuristic",
		Complexity: config.ComplexityConfig{
			Enabled:    true,
			CloudAbove: 40,
			Priority:   75,
		},
	})
	if rule == nil {
		t.Fatal("expected rule")
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{
			Role: "user",
			Content: "architect the system across src/a.go pkg/b.go internal/c.go " +
				"cmd/d.go api/e.go store/f.go with multi-agent plan mode",
		}},
		Tools: []byte(`[{"name":"Shell"},{"name":"Write"},{"name":"read_file"}]`),
	}
	res, err := rule.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched || res.Action == nil || res.Action.Target != "cloud" {
		t.Fatalf("expected cloud match, got %+v", res)
	}
	if req.Metadata.ComplexitySource != "heuristic" || req.Metadata.ComplexityScore < 40 {
		t.Fatalf("metadata not set: %+v", req.Metadata)
	}
}

func TestComplexityRule_DisabledNil(t *testing.T) {
	if router.NewComplexityRule(config.RoutingConfig{}) != nil {
		t.Fatal("disabled complexity should not inject a rule")
	}
}

func TestNewEngineFromConfig_ComplexityDoesNotBeatExplicit(t *testing.T) {
	eng, err := router.NewEngineFromConfig(config.RoutingConfig{
		ComplexityFrom: "heuristic",
		Complexity: config.ComplexityConfig{
			Enabled:    true,
			CloudAbove: 1,
			Priority:   95,
		},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Local",
				Priority: 100,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "/local architect everything with tools"}},
		Tools:    []byte(`[{"name":"Shell"}]`),
	}
	d, err := eng.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" {
		t.Fatalf("explicit /local must win over complexity, got %+v", d)
	}
}

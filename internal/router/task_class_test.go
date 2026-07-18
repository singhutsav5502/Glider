package router_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestTaskClassifier_SmallLocalBeatsTokenOverflow(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true, LocalModel: "codellama:7b"},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Local",
				Priority: 100,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Explicit Cloud",
				Priority: 99,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Context Overflow",
				Priority: 10,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: ">", Value: 8000},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Default Cloud",
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
		Messages: []backend.Message{{Role: "user", Content: "Please rename foo to bar in this file"}},
		Metadata: backend.RequestMetadata{EstimatedTokens: 12000},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" || d.Reason != "small_offload" {
		t.Fatalf("got target=%q reason=%q rule=%q want local/small_offload", d.Target, d.Reason, d.RuleName)
	}
}

func TestTaskClassifier_MustCloudBeatsSmallContext(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Cloud",
				Priority: 99,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Small Context Local",
				Priority: 5,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: "<=", Value: 8000},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default Cloud",
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
		Messages: []backend.Message{{Role: "user", Content: "Please redesign the auth architecture across the codebase"}},
		Metadata: backend.RequestMetadata{EstimatedTokens: 500},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "cloud" || d.Reason != "must_cloud" {
		t.Fatalf("got target=%q reason=%q rule=%q want cloud/must_cloud", d.Target, d.Reason, d.RuleName)
	}
}

func TestTaskClassifier_ToolsForceCloud(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true, LocalModel: "codellama:7b"},
		Rules: []config.RuleConfig{
			{
				Name:     "Small Context Local",
				Priority: 5,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: "<=", Value: 8000},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default Cloud",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tools, _ := json.Marshal([]map[string]any{
		{"type": "function", "function": map[string]any{"name": "read_file"}},
	})
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "rename foo to bar"}},
		Tools:    tools,
		Metadata: backend.RequestMetadata{EstimatedTokens: 100},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "cloud" || d.Reason != "tools_present" {
		t.Fatalf("got target=%q reason=%q want cloud/tools_present", d.Target, d.Reason)
	}
}

func TestTaskClassifier_CloudExplicitWinsOverClassifier(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true, LocalModel: "codellama:7b"},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Local",
				Priority: 100,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Explicit Cloud",
				Priority: 99,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Default Cloud",
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
		Messages: []backend.Message{{Role: "user", Content: "/cloud rename foo to bar"}},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "cloud" || d.RuleName != "Explicit Cloud" {
		t.Fatalf("got target=%q rule=%q want Explicit Cloud", d.Target, d.RuleName)
	}
	if req.Messages[0].Content != "rename foo to bar" {
		t.Fatalf("prefix not stripped: %q", req.Messages[0].Content)
	}
}

func TestTaskClassifier_DisabledNoInject(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: false},
		Rules: []config.RuleConfig{
			{
				Name:     "Context Overflow",
				Priority: 10,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: ">", Value: 8000},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Default Cloud",
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
		Messages: []backend.Message{{Role: "user", Content: "rename foo to bar"}},
		Metadata: backend.RequestMetadata{EstimatedTokens: 12000},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.RuleName != "Context Overflow" {
		t.Fatalf("rule=%q want Context Overflow when classifier off", d.RuleName)
	}
}

func TestInferTaskRole(t *testing.T) {
	if got := router.InferTaskRole("please rename foo to bar"); got != "exec" {
		t.Fatalf("got=%q", got)
	}
	if got := router.InferTaskRole("research compare options for caching"); got != "research" {
		t.Fatalf("got=%q", got)
	}
}

func TestTaskClassifier_LocalExplicitWinsOverTools(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true, LocalModel: "codellama:7b"},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Local",
				Priority: 100,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default Cloud",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := json.Marshal([]map[string]any{
		{"type": "function", "function": map[string]any{"name": "Shell"}},
	})
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "/local run shell"}},
		Tools:    tools,
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" || d.RuleName != "Explicit Local" {
		t.Fatalf("got target=%q rule=%q want Explicit Local", d.Target, d.RuleName)
	}
}


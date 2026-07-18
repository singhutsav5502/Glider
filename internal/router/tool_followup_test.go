package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestDecideToolFollowupAllowlist(t *testing.T) {
	cfg := config.ToolFollowupConfig{
		Enabled:              true,
		InheritParentDefault: boolPtr(true),
		Reevaluate:           boolPtr(true),
		LocalToolAllowlist:   []string{"read_file", "grep", "Glob"},
		CloudToolDenylist:    []string{"Shell", "Write", "Delete"},
	}
	v := DecideToolFollowup(cfg, ToolFollowupSignals{
		ParentRoute: "cloud",
		ToolNames:   []string{"read_file", "grep"},
	})
	if !v.PreferLocal || v.Reason != "allowlist" {
		t.Fatalf("got %+v want prefer local allowlist", v)
	}

	v = DecideToolFollowup(cfg, ToolFollowupSignals{
		ParentRoute: "cloud",
		ToolNames:   []string{"Shell"},
	})
	if v.PreferLocal || !stringsHasPrefix(v.Reason, "denylist:") {
		t.Fatalf("got %+v want denylist cloud", v)
	}

	v = DecideToolFollowup(cfg, ToolFollowupSignals{
		ParentRoute:   "local",
		ToolNames:     []string{"read_file"},
		ExplicitCloud: true,
	})
	if v.PreferLocal || v.Reason != "explicit_cloud" {
		t.Fatalf("got %+v want explicit_cloud", v)
	}
}

func TestDecideToolFollowupInheritWithoutReevaluate(t *testing.T) {
	cfg := config.ToolFollowupConfig{
		Enabled:              true,
		InheritParentDefault: boolPtr(true),
		Reevaluate:           boolPtr(false),
		LocalToolAllowlist:   []string{"read_file"},
	}
	v := DecideToolFollowup(cfg, ToolFollowupSignals{
		ParentRoute: "local",
		ToolNames:   []string{"read_file"},
	})
	if !v.PreferLocal || v.Reason != "inherit_parent_local" {
		t.Fatalf("got %+v", v)
	}
	v = DecideToolFollowup(cfg, ToolFollowupSignals{
		ParentRoute: "cloud",
		ToolNames:   []string{"read_file"},
	})
	if v.PreferLocal || v.Reason != "inherit_parent_cloud" {
		t.Fatalf("got %+v", v)
	}
}

func TestToolsAllLocalAllowedSkipsToolsCloud(t *testing.T) {
	tf := config.ToolFollowupConfig{
		Enabled:            true,
		Reevaluate:         boolPtr(true),
		LocalToolAllowlist: []string{"read_file", "grep"},
		CloudToolDenylist:  []string{"Shell"},
	}
	engine, err := NewEngineFromConfig(config.RoutingConfig{
		ToolFollowup: tf,
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:    true,
			LocalModel: "codellama:7b",
		},
		Rules: []config.RuleConfig{{
			Name: "Always Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := json.Marshal([]map[string]any{
		{"type": "function", "function": map[string]any{"name": "read_file"}},
	})
	d, err := engine.Route(context.Background(), &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "read foo"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" {
		t.Fatalf("allowlisted tools should skip tools→cloud, got target=%s rule=%s", d.Target, d.RuleName)
	}

	tools2, _ := json.Marshal([]map[string]any{
		{"type": "function", "function": map[string]any{"name": "Shell"}},
	})
	d2, err := engine.Route(context.Background(), &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "run ls"}},
		Tools:    tools2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Target != "cloud" {
		t.Fatalf("Shell must stay tools→cloud, got target=%s", d2.Target)
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

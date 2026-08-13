package router_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestMatchExplicitCommand_PrefixAndLineAndToken(t *testing.T) {
	cmds := []string{"/cloud", "/heavy", "/local", "/fast"}

	cmd, rest, ok := router.MatchExplicitCommand("/cloud do work", cmds)
	if !ok || cmd != "/cloud" || rest != "do work" {
		t.Fatalf("prefix: cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}

	cmd, rest, ok = router.MatchExplicitCommand("chrome label\n/cloud hello", cmds)
	if !ok || cmd != "/cloud" || rest != "chrome label\nhello" {
		t.Fatalf("line: cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}

	cmd, rest, ok = router.MatchExplicitCommand("please use /cloud for this rename", cmds)
	if !ok || cmd != "/cloud" || !strings.Contains(rest, "please use") || !strings.Contains(rest, "for this rename") {
		t.Fatalf("token: cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}

	if _, _, ok := router.MatchExplicitCommand("/cloudiness bad", cmds); ok {
		t.Fatal("must not match /cloudiness as /cloud")
	}
}

func TestExplicitHardOverride_BeatsInvertedClassifierPriority(t *testing.T) {
	// Simulate misconfig: small-local classifier priority ABOVE explicit rules.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:            true,
			LocalModel:         "codellama:7b",
			SmallLocalPriority: 200, // higher than Explicit Cloud 99
		},
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Cloud",
				Priority: 99,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Always Local",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"/cloud rename foo to bar",
		"TipTap chrome\n/cloud rename foo to bar",
		"please /cloud rename foo to bar",
	}
	for _, content := range cases {
		req := &backend.CompletionRequest{
			Messages: []backend.Message{{Role: "user", Content: content}},
		}
		d, err := engine.Route(testCtx(), req)
		if err != nil {
			t.Fatalf("%q: %v", content, err)
		}
		if d.Target != "cloud" || d.RuleName != "Explicit Cloud" {
			t.Fatalf("%q: target=%q rule=%q want Explicit Cloud", content, d.Target, d.RuleName)
		}
		if d.Reason != "explicit_override" {
			t.Fatalf("%q: reason=%q want explicit_override", content, d.Reason)
		}
		if router.HasCloudOverride(req.Messages[0].Content) {
			t.Fatalf("%q: /cloud should be stripped, got %q", content, req.Messages[0].Content)
		}
	}
}

func TestHasCloudOverride(t *testing.T) {
	if !router.HasCloudOverride("/cloud hi") {
		t.Fatal("expected HasCloudOverride")
	}
	if !router.HasCloudOverride("x\n/heavy y") {
		t.Fatal("expected /heavy")
	}
	if router.HasCloudOverride("/local only") {
		t.Fatal("/local is not cloud override")
	}
}

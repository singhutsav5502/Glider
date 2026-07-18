package router_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
)

func TestMatchComposerWrapupDetectsChrome(t *testing.T) {
	body := []byte("Friday stock market close\x00user_visible_high_level_summary\x00Markets closed")
	if !router.MatchComposerWrapup("Friday stock market close", body) {
		t.Fatal("want MatchComposerWrapup for user_visible_high_level_summary body")
	}
	if router.MatchComposerWrapup("rename foo to bar in helpers.go please", nil) {
		t.Fatal("real user instruction must not match wrap-up")
	}
}

func TestComposerWrapupOriginRuleBeatsSmallContext(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: router.ComposerWrapupRuleName, Priority: 95,
				Trigger: config.TriggerConfig{Type: "composer_wrapup"},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Small Context Local", Priority: 5,
				Trigger: config.TriggerConfig{Type: "context_size", Operator: "<=", Value: 8000},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "Stock market today status"}},
		Metadata: backend.RequestMetadata{
			EstimatedTokens: 120,
			ExtractSource:   "printable_hint",
			WrapupScan:      "user_visible_high_level_summary\x00Markets are closed Saturday",
			LastRouteCloud:  true,
		},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "cloud" || d.RuleName != router.ComposerWrapupRuleName {
		t.Fatalf("got target=%q rule=%q want cloud/%s", d.Target, d.RuleName, router.ComposerWrapupRuleName)
	}
}

func TestComposerWrapupOriginRuleStickyCloudNonFresh(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: router.ComposerWrapupRuleName, Priority: 95,
				Trigger: config.TriggerConfig{Type: router.TriggerComposerWrapup},
				Action:  config.ActionConfig{Target: "cloud"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "m"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "Hi!"}},
		Metadata: backend.RequestMetadata{
			StickyCloudLive: true,
			ExtractSource:   "printable_hint",
		},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "cloud" {
		t.Fatalf("sticky cloud non-fresh crumb must route cloud, got %q", d.Target)
	}
}

func TestComposerWrapupOriginRuleAllowsFreshTipTap(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: router.ComposerWrapupRuleName, Priority: 95,
				Trigger: config.TriggerConfig{Type: "composer_wrapup"},
				Action:  config.ActionConfig{Target: "cloud"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "m"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "rename foo to bar across the module"}},
		Metadata: backend.RequestMetadata{
			StickyCloudLive: true,
			ExtractSource:   "tiptap_text",
			WrapupScan:      `{"type":"doc","content":[{"type":"text","text":"rename foo to bar across the module"}]}`,
		},
	}
	d, err := engine.Route(testCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" || d.RuleName != "Always Local" {
		t.Fatalf("fresh TipTap user turn must re-decide local, got target=%q rule=%q", d.Target, d.RuleName)
	}
}

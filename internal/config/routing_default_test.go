package config_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/config"
)

func TestApplyDefaultTargetLocal(t *testing.T) {
	r := config.RoutingConfig{
		Default:           "local",
		DefaultLocalModel: "codellama:7b",
		Rules: []config.RuleConfig{{
			Name:     "Default Origin",
			Priority: 0,
			Trigger:  config.TriggerConfig{Type: "always"},
			Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}
	r.ApplyDefaultTarget()
	if r.Rules[0].Action.Target != "local" || r.Rules[0].Name != "Default Local" {
		t.Fatalf("got %+v", r.Rules[0])
	}
	if !r.AllowCloudFallbackOrDefault() {
		t.Fatal("default allow cloud fallback should still be true when unset")
	}
	f := false
	r.AllowCloudFallback = &f
	if r.AllowCloudFallbackOrDefault() {
		t.Fatal("expected false")
	}
}

func TestOriginOnLocalErrorDefault(t *testing.T) {
	m := config.MITMConfig{}
	if !m.OriginOnLocalErrorOrDefault() {
		t.Fatal("hybrid default should fail-soft to origin")
	}
	f := false
	m.OriginOnLocalError = &f
	if m.OriginOnLocalErrorOrDefault() {
		t.Fatal("pure-local should disable origin fail-soft")
	}
}

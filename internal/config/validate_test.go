package config_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/config"
)

func TestValidateLogLevel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.LogLevel = "nope"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateGPUAssignments(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.VRAM.GPUAssignments = map[string]int{"codellama:7b": 3}
	res := config.ValidateDetailed(cfg, config.ValidateOptions{GPUCount: 1, Soft: true})
	if res.Err() == nil {
		t.Fatal("expected GPU out of range error")
	}
}

func TestValidateCatalogSoft(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelConfig{{Name: "missing:model", Backend: "ollama"}}
	catalog := config.NewModelCatalog("codellama:7b")
	res := config.ValidateDetailed(cfg, config.ValidateOptions{Catalog: catalog, Soft: true})
	if res.Err() != nil {
		t.Fatalf("soft should not hard-fail: %v", res.Err())
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning for missing model")
	}
}

func TestValidateRuleRequiresCommands(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Routing.Rules = []config.RuleConfig{{
		Name:    "bad",
		Trigger: config.TriggerConfig{Type: "explicit"},
		Action:  config.ActionConfig{Target: "local"},
	}}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error")
	}
}

func TestRuleIsEnabled(t *testing.T) {
	r := config.RuleConfig{}
	if !r.IsEnabled() {
		t.Fatal("nil enabled should default true")
	}
	f := false
	r.Enabled = &f
	if r.IsEnabled() {
		t.Fatal("expected disabled")
	}
}

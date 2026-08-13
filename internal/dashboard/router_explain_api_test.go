package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/glider-ai/glider/internal/config"
)

func postRouterExplain(t *testing.T, baseURL string, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(baseURL+"/api/router/explain", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return out
}

func TestRouterExplain_ExplicitOverride_Wins(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.TaskClassifier = config.TaskClassifierConfig{} // default test config enables this; keep this test to just the two rules under test
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "Force Cloud",
			Priority: 100,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		},
		{
			Name:     "Default Local",
			Priority: 0,
			Trigger:  config.TriggerConfig{Type: "always"},
			Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
		},
	}
	provider.SwapForTest(cfg)

	out := postRouterExplain(t, ts.URL, map[string]any{"text": "/cloud please help"})
	decision := out["decision"].(map[string]any)
	if decision["ruleName"] != "Force Cloud" || decision["target"] != "cloud" {
		t.Fatalf("got decision %+v", decision)
	}
	entries := out["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected an entry for every configured rule, got %+v", entries)
	}
}

func TestRouterExplain_ShowsShadowedRule(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "Force Cloud A",
			Priority: 100,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud"},
		},
		{
			Name:     "Force Cloud B",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud"},
		},
	}
	provider.SwapForTest(cfg)

	out := postRouterExplain(t, ts.URL, map[string]any{"text": "do it /cloud"})
	var sawShadowed bool
	for _, e := range out["entries"].([]any) {
		entry := e.(map[string]any)
		if entry["ruleName"] == "Force Cloud B" {
			if entry["matched"] != true || entry["shadowed"] != true {
				t.Fatalf("expected Force Cloud B matched+shadowed, got %+v", entry)
			}
			sawShadowed = true
		}
	}
	if !sawShadowed {
		t.Fatalf("did not find Force Cloud B in entries: %+v", out["entries"])
	}
}

func TestRouterExplain_NoMatch_NilDecisionNoError(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "Refactor",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`},
			Action:   config.ActionConfig{Target: "local"},
		},
	}
	provider.SwapForTest(cfg)

	out := postRouterExplain(t, ts.URL, map[string]any{"text": "hello there"})
	if out["decision"] != nil {
		t.Fatalf("expected nil decision, got %+v", out["decision"])
	}
}

func TestRouterExplain_ScriptRule_SkippedNotFatal(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "Swarm Script",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "script", File: "scripts/examples/fanout_dual_view.star"},
			Action:   config.ActionConfig{Target: "local"},
		},
	}
	provider.SwapForTest(cfg)

	out := postRouterExplain(t, ts.URL, map[string]any{"text": "hello"})
	skipped, _ := out["skippedRules"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("expected the script rule to be reported skipped, not silently dropped, got %+v", out)
	}
}

func TestRouterExplain_ToolsPresent_TriggersToolsClassifier(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.TaskClassifier = config.TaskClassifierConfig{Enabled: true}
	provider.SwapForTest(cfg)

	out := postRouterExplain(t, ts.URL, map[string]any{"text": "run the tool", "tools": []string{"bash"}})
	decision := out["decision"].(map[string]any)
	if decision["ruleName"] != "Task Classifier Tools→Cloud" || decision["target"] != "cloud" {
		t.Fatalf("got decision %+v", decision)
	}
}

func TestRouterExplain_InvalidBody_Returns400(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Post(ts.URL+"/api/router/explain", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestRouterLint_FindsDuplicateExplicitCommand(t *testing.T) {
	ts, provider, _, _ := setupDash(t)
	cfg := provider.Get()
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "Force Cloud A",
			Priority: 100,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud"},
		},
		{
			Name:     "Force Cloud B",
			Priority: 50,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
			Action:   config.ActionConfig{Target: "cloud"},
		},
	}
	provider.SwapForTest(cfg)

	resp, err := http.Get(ts.URL + "/api/router/lint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	findings := out["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	f := findings[0].(map[string]any)
	if f["example"] != "/cloud example message" {
		t.Fatalf("got %+v", f)
	}
}

func TestRouterLint_CleanConfig_NoFindings(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/router/lint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings, ok := out["findings"].([]any); ok && len(findings) != 0 {
		t.Fatalf("expected no findings for default test config, got %+v", findings)
	}
}

func TestRouterLint_WrongMethod_Returns405(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Post(ts.URL+"/api/router/lint", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

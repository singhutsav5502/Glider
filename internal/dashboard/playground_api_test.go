package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/vendors"
)

// playgroundParse posts text to /api/playground/parse and decodes the
// response into a generic map — the tests below only assert on the
// specific fields each case cares about, matching how the frontend
// consumes this endpoint.
func playgroundParse(t *testing.T, baseURL, text string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	resp, err := http.Post(baseURL+"/api/playground/parse", "application/json", bytes.NewReader(body))
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

func TestPlaygroundParse_DelegateFlag_HeadlessRun(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Binary: "agy", Path: filepath.Join("fake", "agy.exe"), PrintFlag: "-p", Enabled: true,
			Templates: []vendors.CommandTemplate{{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"}}},
	}})
	ts, _, _, _ := setupDash(t)

	out := playgroundParse(t, ts.URL, "summarize recent commits /agy")
	delegate := out["delegate"].(map[string]any)
	if delegate["matched"] != true || delegate["kind"] != "run" || delegate["vendor"] != "agy" {
		t.Fatalf("got %+v", delegate)
	}
	if delegate["prompt"] != "summarize recent commits" {
		t.Fatalf("got prompt %+v", delegate["prompt"])
	}
	// Not a leading flag, and not any other family — workspace/routing stay unmatched.
	if out["workspace"].(map[string]any)["matched"] != false {
		t.Fatalf("workspace should not have matched: %+v", out["workspace"])
	}
}

func TestPlaygroundParse_DelegateFlag_InteractiveTemplate(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Binary: "agy", Path: filepath.Join("fake", "agy.exe"), PrintFlag: "-p", Enabled: true,
			Templates: []vendors.CommandTemplate{
				{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"},
				{Name: "interactive", Args: []string{"{{prompt}}"}, Mode: "interactive"},
			}},
	}})
	ts, _, _, _ := setupDash(t)

	out := playgroundParse(t, ts.URL, "fix the auth bug /agy:interactive")
	delegate := out["delegate"].(map[string]any)
	if delegate["matched"] != true || delegate["kind"] != "interactive" || delegate["template"] != "interactive" {
		t.Fatalf("got %+v", delegate)
	}
}

func TestPlaygroundParse_DelegateFlag_UnknownTemplate(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Binary: "agy", Path: filepath.Join("fake", "agy.exe"), PrintFlag: "-p", Enabled: true,
			Templates: []vendors.CommandTemplate{{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"}}},
	}})
	ts, _, _, _ := setupDash(t)

	out := playgroundParse(t, ts.URL, "do the thing /agy:bogus")
	delegate := out["delegate"].(map[string]any)
	if delegate["matched"] != true || delegate["kind"] != "unknown_template" {
		t.Fatalf("got %+v", delegate)
	}
}

func TestPlaygroundParse_AllowDenyTokens(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Binary: "agy", Path: filepath.Join("fake", "agy.exe"), PrintFlag: "-p", Enabled: true,
			Templates: []vendors.CommandTemplate{{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"}}},
	}})
	ts, _, _, _ := setupDash(t)

	allow := playgroundParse(t, ts.URL, "abc123 /agy:allow")
	d := allow["delegate"].(map[string]any)
	if d["matched"] != true || d["kind"] != "allow" || d["prompt"] != "abc123" {
		t.Fatalf("got %+v", d)
	}

	deny := playgroundParse(t, ts.URL, "abc123 /agy:deny")
	d = deny["delegate"].(map[string]any)
	if d["matched"] != true || d["kind"] != "deny" || d["prompt"] != "abc123" {
		t.Fatalf("got %+v", d)
	}
}

func TestPlaygroundParse_WorkspaceFlag(t *testing.T) {
	isolateVendorRegistry(t)
	ts, _, _, _ := setupDash(t)

	out := playgroundParse(t, ts.URL, `C:\Users\me\proj /workspace`)
	ws := out["workspace"].(map[string]any)
	if ws["matched"] != true || ws["path"] != `C:\Users\me\proj` {
		t.Fatalf("got %+v", ws)
	}
}

func TestPlaygroundParse_RoutingOverride_UsesRealConfiguredCommands(t *testing.T) {
	isolateVendorRegistry(t)
	ts, provider, _, _ := setupDash(t)

	cfg := provider.Get()
	cfg.Routing.Rules = []config.RuleConfig{
		{
			Name:     "force-local",
			Priority: 100,
			Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local", "/fast"}},
			Action:   config.ActionConfig{Target: "local"},
		},
	}
	provider.SwapForTest(cfg)

	out := playgroundParse(t, ts.URL, "/local please keep this on-device")
	routing := out["routing"].(map[string]any)
	if routing["matched"] != true || routing["command"] != "/local" || routing["ruleName"] != "force-local" || routing["target"] != "local" {
		t.Fatalf("got %+v", routing)
	}
	configured, _ := routing["configuredCommands"].([]any)
	if len(configured) != 2 {
		t.Fatalf("expected the real configured commands echoed back, got %+v", routing["configuredCommands"])
	}
}

func TestPlaygroundParse_NoMatch_ReturnsAllUnmatchedNotError(t *testing.T) {
	isolateVendorRegistry(t)
	ts, _, _, _ := setupDash(t)

	out := playgroundParse(t, ts.URL, "just a normal chat message, nothing special here")
	if out["delegate"].(map[string]any)["matched"] != false {
		t.Fatalf("delegate should not have matched: %+v", out["delegate"])
	}
	if out["workspace"].(map[string]any)["matched"] != false {
		t.Fatalf("workspace should not have matched: %+v", out["workspace"])
	}
	if out["routing"].(map[string]any)["matched"] != false {
		t.Fatalf("routing should not have matched: %+v", out["routing"])
	}
}

func TestPlaygroundParse_InvalidBody_Returns400(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Post(ts.URL+"/api/playground/parse", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestPlaygroundParse_WrongMethod_Returns405(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/playground/parse")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

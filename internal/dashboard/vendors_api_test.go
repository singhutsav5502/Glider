package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

// isolateVendorRegistry redirects vendors.DefaultRegistryPath() (which
// resolves under the real user's home directory) to a per-test temp dir,
// via the same env vars os.UserHomeDir() itself reads — so these tests
// never touch the real ~/.glider/vendors.json a live Glider install (or
// this very session's own live-testing) may have written.
func isolateVendorRegistry(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir) // windows
	t.Setenv("HOME", dir)        // unix
}

func writeTestRegistry(t *testing.T, reg vendors.Registry) {
	t.Helper()
	path, err := vendors.DefaultRegistryPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := vendors.SaveRegistry(path, reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVendorsAPI_SetTemplates_ReplacesAndPersists(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Binary: "agy", Path: filepath.Join("fake", "agy.exe"), PrintFlag: "-p", Enabled: true},
	}})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{
		"templates": []vendors.CommandTemplate{
			{Name: "default", Args: []string{"-p", "--add-dir={{cwd}}", "{{prompt}}"}, Mode: "headless"},
			{Name: "resume", Args: []string{"-p", "--continue", "{{prompt}}"}, Mode: "headless"},
		},
	})
	resp, err := http.Post(ts.URL+"/api/vendors/agy/templates", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}

	var reg vendors.Registry
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := reg.Find("agy")
	if !ok || len(v.Templates) != 2 || v.Templates[0].Name != "default" {
		t.Fatalf("got %+v", v)
	}

	// Persisted, not just returned — a fresh load must see the same data.
	path, _ := vendors.DefaultRegistryPath()
	reloaded, err := vendors.LoadRegistry(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v2, ok := reloaded.Find("agy")
	if !ok || len(v2.Templates) != 2 {
		t.Fatalf("got %+v", v2)
	}
}

func TestVendorsAPI_SetTemplates_UnknownVendor404s(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{"templates": []vendors.CommandTemplate{{Name: "default", Args: []string{"-p"}}}})
	resp, err := http.Post(ts.URL+"/api/vendors/does-not-exist/templates", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestVendorsAPI_SetTemplates_RejectsBlankName(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{{Name: "agy", Enabled: true}}})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{"templates": []vendors.CommandTemplate{{Name: "", Args: []string{"-p"}}}})
	resp, err := http.Post(ts.URL+"/api/vendors/agy/templates", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestVendorsAPI_SetTemplates_RejectsDuplicateName(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{{Name: "agy", Enabled: true}}})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{"templates": []vendors.CommandTemplate{
		{Name: "default", Args: []string{"-p"}},
		{Name: "default", Args: []string{"-p", "--x"}},
	}})
	resp, err := http.Post(ts.URL+"/api/vendors/agy/templates", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestVendorsAPI_SetDefaultWorkspace_PersistsAndAppliesLive(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]string{"path": `C:\Users\me\project`})
	resp, err := http.Post(ts.URL+"/api/vendors/workspace", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}

	path, _ := vendors.DefaultRegistryPath()
	reloaded, err := vendors.LoadRegistry(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reloaded.DefaultWorkspace != `C:\Users\me\project` {
		t.Fatalf("got persisted DefaultWorkspace %q", reloaded.DefaultWorkspace)
	}
	if vendors.DefaultWorkspace() != `C:\Users\me\project` {
		t.Fatalf("got live in-memory default %q, expected it applied immediately", vendors.DefaultWorkspace())
	}
	vendors.SetDefaultWorkspace("") // don't leak into other tests
}

func TestVendorsAPI_SetResponseDetail_PersistsAndAppliesLive(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{})
	defer vendors.SetResponseDetail(vendors.ResponseDetailClean) // don't leak into other tests

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]string{"mode": "raw"})
	resp, err := http.Post(ts.URL+"/api/vendors/response-detail", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}

	path, _ := vendors.DefaultRegistryPath()
	reloaded, err := vendors.LoadRegistry(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reloaded.ResponseDetail != vendors.ResponseDetailRaw {
		t.Fatalf("got persisted ResponseDetail %q", reloaded.ResponseDetail)
	}
	if vendors.ResponseDetail() != vendors.ResponseDetailRaw {
		t.Fatalf("got live in-memory mode %q, expected it applied immediately", vendors.ResponseDetail())
	}
}

func TestVendorsAPI_SetResponseDetail_RejectsUnknownMode(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]string{"mode": "verbose"})
	resp, err := http.Post(ts.URL+"/api/vendors/response-detail", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for an unrecognized mode", resp.StatusCode)
	}
}

// TestVendorsAPI_Discover_PreservesDefaultWorkspace guards the real bug
// this endpoint's implementation had to explicitly avoid: Discover()
// builds a fresh Registry with no DefaultWorkspace field at all, so a
// naive save-after-discover would silently wipe out a previously
// configured default on every "Rescan" click.
func TestVendorsAPI_Discover_PreservesDefaultWorkspace(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{DefaultWorkspace: `C:\Users\me\project`})
	// candidatesPath() resolves configs/vendor_candidates.yaml relative to
	// cwd, matching production (Glider runs from the repo/install root) —
	// this test's package dir isn't that root, so point cwd at the real
	// repo root for this one test; t.Chdir restores it automatically.
	t.Chdir("../..")

	ts, _, _, _ := setupDash(t)

	resp, err := http.Post(ts.URL+"/api/vendors/discover", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var reg vendors.Registry
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.DefaultWorkspace != `C:\Users\me\project` {
		t.Fatalf("got DefaultWorkspace %q after discover, expected it preserved", reg.DefaultWorkspace)
	}
}

func TestVendorsAPI_List_IncludesTemplates(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Enabled: true, Templates: []vendors.CommandTemplate{{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"}}},
	}})

	ts, _, _, _ := setupDash(t)

	resp, err := http.Get(ts.URL + "/api/vendors")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var reg vendors.Registry
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := reg.Find("agy")
	if !ok || len(v.Templates) != 1 {
		t.Fatalf("got %+v", v)
	}
}

func TestVendorsAPI_GetPermissions_ClaudeDefaultsToAsk(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "claude", Enabled: true, Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-p", "{{prompt}}"}},
		}},
	}})

	ts, _, _, _ := setupDash(t)

	resp, err := http.Get(ts.URL + "/api/vendors/claude/permissions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	var out struct {
		Presets []vendors.PermissionPreset `json:"presets"`
		Current string                     `json:"current"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Current != "ask" {
		t.Fatalf("got current=%q, want \"ask\"", out.Current)
	}
	if len(out.Presets) != 2 {
		t.Fatalf("got %d presets for claude, want 2", len(out.Presets))
	}
}

func TestVendorsAPI_SetPermissions_ClaudeTrustSessionPersists(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "claude", Enabled: true, Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-p", "{{prompt}}"}},
			{Name: "resume", Mode: "headless", Args: []string{"-p", "--resume", "{{session_id}}", "{{prompt}}"}},
		}},
	}})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{"preset": "trust_session"})
	resp, err := http.Post(ts.URL+"/api/vendors/claude/permissions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}

	path, _ := vendors.DefaultRegistryPath()
	reloaded, err := vendors.LoadRegistry(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := reloaded.Find("claude")
	if !ok {
		t.Fatalf("vendor not found after preset apply")
	}
	for _, tmpl := range v.Templates {
		found := false
		for _, a := range tmpl.Args {
			if a == "--dangerously-skip-permissions" {
				found = true
			}
		}
		if !found {
			t.Fatalf("template %q missing the skip flag after trust_session preset: %v", tmpl.Name, tmpl.Args)
		}
	}
}

func TestVendorsAPI_SetPermissions_MissingWorkspace400s(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Enabled: true},
	}})

	ts, _, _, _ := setupDash(t)

	body, _ := json.Marshal(map[string]any{"preset": "trust_folder"})
	resp, err := http.Post(ts.URL+"/api/vendors/agy/permissions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 — no workspace given and no default configured", resp.StatusCode)
	}
}

func TestVendorsAPI_LaunchInteractive_UnknownVendor404s(t *testing.T) {
	isolateVendorRegistry(t)
	writeTestRegistry(t, vendors.Registry{})

	ts, _, _, _ := setupDash(t)

	resp, err := http.Post(ts.URL+"/api/vendors/does-not-exist/launch-interactive", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}
